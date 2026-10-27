package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"unicode/utf8"

	"github.com/aloutfi/actions/ci"
	"github.com/aloutfi/actions/core"
	"github.com/aloutfi/actions/redact"
	"github.com/aloutfi/actions/vcs"
)

// gate decides whether this merge-time run applies, before anything holds
// cloud credentials: the superseded-tree guard and, opt-in
// (TFPR_SKIP_NOOP=true), skip on no-op. It needs only the repo host's token.
// It writes decision.json to the plan dir and sets the outputs apply,
// action, reason, pr_number, plan_run_id, download and artifact_name. A
// superseded commit is a clean skip (exit 0); an unverifiable ref fails.
func (a *app) gate() error {
	a.loadCI()
	v, err := a.loadVCS()
	if err != nil {
		return err
	}
	// Start empty so apply only ever sees what this run downloads.
	if dir := os.Getenv("TFPR_PLAN_DIR"); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	g := a.runGate(v)
	if err := a.writeDecision(g); err != nil {
		return err
	}
	// Only a trusted record's run (planned for the PR's final head) supplies a
	// planfile; without one, apply finds no planfile and fails closed.
	download := g.Action == core.GateApply && os.Getenv("TFPR_USE_PLANFILE") == "true" &&
		core.IsFirstAttempt(g.Attempt) && g.PlanRun != ""
	if err := a.outputs(
		core.Field{Name: "apply", Value: strconv.FormatBool(g.Action == core.GateApply)},
		core.Field{Name: "action", Value: g.Action},
		core.Field{Name: "reason", Value: g.Reason},
		core.Field{Name: "pr_number", Value: g.PR},
		core.Field{Name: "plan_run_id", Value: g.PlanRun},
		core.Field{Name: "download", Value: strconv.FormatBool(download)},
		core.Field{Name: "artifact_name", Value: g.Artifact},
	); err != nil {
		return err
	}
	if g.Action == core.GateRefuse {
		return exitCode(1) // the guard logged why
	}
	return nil
}

// runGate gathers the facts and applies core.Gate.
func (a *app) runGate(v vcs.VCS) core.GateResult {
	c := a.ctx
	branchHead := func(branch string) (string, error) {
		head, err := v.BranchHead(branch)
		if err != nil {
			fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		}
		return head, err
	}
	guard := core.SupersededGuard(c.Ref, c.SHA, c.Attempt, branchHead)
	a.log(guard.Level, guard.Message)
	g := core.GateResult{
		Schema: core.DecisionSchema, Stack: a.stack, Workspace: a.workspace,
		SHA: c.SHA, Ref: c.Ref, BranchHead: guard.Head, Attempt: c.Attempt,
		Artifact: core.ArtifactName(a.stack, a.workspace),
	}
	in := core.GateInput{
		Guard: guard, Key: core.Key(a.stack, a.workspace), Attempt: c.Attempt,
		SkipNoop: os.Getenv("TFPR_SKIP_NOOP") == "true",
	}
	if guard.Code == core.GuardOK {
		// The PR and its record also say which run planned it (for the
		// planfile download). Failures here are doubts: they apply.
		prs, err := v.PRsForCommit(c.SHA, c.Ref)
		if err != nil {
			a.ci.Log(ci.Warning, fmt.Sprintf("Could not look up the PR that merged %s: %s", c.SHA, err))
		}
		in.PRs = len(prs)
		if len(prs) == 1 {
			g.PR, in.PRHead = strconv.Itoa(prs[0].Number), prs[0].HeadSHA
			recs, err := v.Records(g.PR, core.PlanMarker(a.stack, a.workspace))
			if err != nil {
				a.ci.Log(ci.Warning, fmt.Sprintf("Could not read the plan record on PR #%s: %s", g.PR, err))
			}
			in.Records = recs
			if rec, doubt := core.TrustedRecord(recs, in.Key, in.PRHead); doubt == "" {
				g.PlanRun, g.PRHead, g.PlannedSHA = rec.Run, rec.HeadSHA, rec.SHA
			} else if os.Getenv("TFPR_USE_PLANFILE") == "true" {
				a.ci.Log(ci.Warning, fmt.Sprintf("No reviewed planfile for PR #%s's head %s: %s.", g.PR, core.Short(in.PRHead), doubt))
			}
		}
		if in.SkipNoop {
			in.MergeTree = gitTree(c.SHA)
		}
	}
	d := core.Gate(in)
	g.Action, g.Reason = d.Action, d.Reason
	switch {
	case d.Reason == "no-op":
		a.ci.Log(ci.Notice, fmt.Sprintf("Skipping the apply of '%s': the reviewed plan had no changes, and %s's tree is the tree that was planned.", a.stack, core.Short(c.SHA)))
	case d.Action == core.GateApply && in.SkipNoop:
		a.ci.Log(ci.Notice, fmt.Sprintf("Applying '%s': it can't be skipped as a no-op (%s).", a.stack, d.Reason))
	}
	return g
}

// decisionPath is decision.json in the plan dir.
func (a *app) decisionPath() string { return filepath.Join(a.planDir(), "decision.json") }

func (a *app) writeDecision(g core.GateResult) error {
	if err := a.checkPlanDir(); err != nil {
		return err
	}
	if err := os.MkdirAll(a.planDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.decisionPath(), append(data, '\n'), 0o644)
}

// readDecision is the gate's decision for this commit, stack and workspace,
// if a gate wrote one.
func (a *app) readDecision() (core.GateResult, bool) {
	var g core.GateResult
	data, err := os.ReadFile(a.decisionPath())
	if err != nil || json.Unmarshal(data, &g) != nil {
		return g, false
	}
	return g, g.For(a.ctx.SHA, a.stack, a.workspace)
}

// report posts the result of an apply the caller ran itself ("bring your own
// apply") on the PR that merged this commit. TFPR_APPLY_EXIT_CODE is its exit
// code; a skip or refusal comes from the gate's decision.json instead.
// TFPR_APPLY_LOG names its output, which is filtered and cut to fit. It needs
// only the repo host's token.
func (a *app) report() error {
	a.loadCI()
	v, err := a.loadVCS()
	if err != nil {
		return err
	}
	g, ok := a.readDecision()
	if !ok { // no gate ran: the caller decided to apply on its own
		g = core.GateResult{Action: core.GateApply, SHA: a.ctx.SHA, Ref: a.ctx.Ref}
	}
	r := core.ApplyReport{Gate: g}
	if g.Action == core.GateApply {
		s := os.Getenv("TFPR_APPLY_EXIT_CODE")
		if r.ExitCode, err = strconv.Atoi(s); err != nil {
			return fmt.Errorf("TFPR_APPLY_EXIT_CODE must be the exit code of your terraform apply, got '%s' (the gate decided to apply)", s)
		}
	}
	if path := os.Getenv("TFPR_APPLY_LOG"); path != "" && g.Action == core.GateApply {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("TFPR_APPLY_LOG: %w", err)
		}
		text, err := a.filter.Apply(string(data))
		if err != nil {
			return fmt.Errorf("%w; nothing was posted", err)
		}
		r.Log = text.String()
	}
	if err := a.publishApply(v, r); err != nil {
		if errors.Is(err, redact.ErrFailed) || errors.Is(err, vcs.ErrNoAuthor) {
			return fmt.Errorf("%w; nothing was posted", err)
		}
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		a.ci.Log(ci.Warning, "Could not post the apply-result comment.")
	}
	return nil
}

// publishApply upserts the apply-result comment on the PR that merged this
// commit, if there is one.
func (a *app) publishApply(v vcs.VCS, r core.ApplyReport) error {
	sha := a.ctx.SHA
	pr := r.Gate.PR
	if pr == "" {
		// Not every apply comes from a PR merge (drift, manual dispatch).
		prs, err := v.PRsForCommit(sha, a.ctx.Ref)
		if err != nil || len(prs) == 0 {
			fmt.Fprintf(a.stdout, "No PR associated with %s; skipping apply-result comment.\n", sha)
			return nil
		}
		pr = strconv.Itoa(prs[0].Number)
	}
	if r.Gate.Reason == "superseded" {
		r.SupersededBy = a.ci.RunFor(r.Gate.BranchHead)
	}
	marker := core.ApplyMarker(a.stack, a.workspace)
	r.Stack, r.Workspace, r.SHA, r.JobURL, r.ToolVersion = a.stack, a.workspace, sha, a.ci.JobURL(), version
	r.Host = a.commentHost()
	r.Budget = r.Host.MaxComment
	if r.Host.HiddenHTML {
		r.Budget -= utf8.RuneCountInString(marker) + 1
	}
	raw, err := core.RenderApplyReport(r)
	if err != nil {
		return err
	}
	body, err := a.filter.Apply(raw)
	if err != nil {
		return err
	}
	msg, err := v.UpsertComment(vcs.Comment{PR: pr, Marker: marker, Body: body, Resolved: r.Resolved()})
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, msg)
	return nil
}
