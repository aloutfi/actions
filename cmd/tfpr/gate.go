package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	if _, err := core.ParsePaths(os.Getenv("TFPR_PATHS")); err != nil {
		return err
	}
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
	touched := func(head string) ([]string, error) {
		paths, err := a.stackPaths()
		if err != nil {
			fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
			return nil, err
		}
		files, err := v.ChangedFiles(c.SHA, head)
		if err != nil {
			fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
			return nil, err
		}
		return core.Touching(files, paths), nil
	}
	guard := core.SupersededGuard(c.Ref, c.SHA, c.Attempt, branchHead, touched)
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
	// The PR that merged the commit: where the result is posted (also for a
	// skip or refusal), and, with its record, which run planned it (for
	// the planfile download). A failed lookup is a doubt: it applies.
	prs := a.mergedPRs(v)
	g.PRs, in.PRs = make([]string, len(prs)), len(prs)
	for i, p := range prs {
		g.PRs[i] = strconv.Itoa(p.Number)
	}
	if len(prs) == 1 {
		g.PR = g.PRs[0]
	}
	if guard.Code == core.GuardOK {
		if len(prs) == 1 {
			in.PRHead = prs[0].HeadSHA
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

// stackPaths are the repo paths whose changes supersede an older commit's
// apply: the stack directory, the local modules it calls (source = "./..."
// or "../...", followed recursively), and TFPR_PATHS, all relative to the
// repository root. It fails if git can't place the stack in the repository:
// a guessed path that matches nothing would let an older tree apply.
func (a *app) stackPaths() ([]string, error) {
	extra, err := core.ParsePaths(os.Getenv("TFPR_PATHS"))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	cmd := exec.Command("git", "rev-parse", "--show-prefix")
	cmd.Dir, cmd.Stdout, cmd.Env = a.stack, &out, redact.WithoutTFPR(os.Environ())
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git could not place stack '%s' in the repository: %w", a.stack, err)
	}
	prefix := strings.TrimSpace(out.String())
	paths := []string{path.Clean("./" + prefix)}
	for _, dir := range localModules(a.stack) {
		if p := path.Join(prefix, dir); p != ".." && !strings.HasPrefix(p, "../") {
			paths = append(paths, p)
		}
	}
	return append(paths, extra...), nil
}

// localSource is a module call's local source, "./x" or "../x".
var localSource = regexp.MustCompile(`(?m)^\s*source\s*=\s*"(\.\.?/[^"]*)"`)

// localModules are the directories of the local modules stack calls,
// directly or through other local modules, relative to stack
// (slash-separated). It reads the .tf files the way terraform loads a
// module: the directory's own, not its subdirectories'.
func localModules(stack string) []string {
	seen := map[string]bool{".": true}
	var dirs []string
	queue := []string{"."}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		files, _ := filepath.Glob(filepath.Join(stack, filepath.FromSlash(dir), "*.tf"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, m := range localSource.FindAllStringSubmatch(string(data), -1) {
				d := path.Join(dir, m[1])
				if seen[d] {
					continue
				}
				seen[d] = true
				if fi, err := os.Stat(filepath.Join(stack, filepath.FromSlash(d))); err == nil && fi.IsDir() {
					dirs = append(dirs, d)
					queue = append(queue, d)
				}
			}
		}
	}
	return dirs
}

// renderApply renders the apply-result body within the comment budget
// (TFPR_COMMENT_BUDGET, at most the host's limit) and filters it. If the
// filter grows it past the budget, the apply log's tail is trimmed further
// and the body rendered again; failing that, the body goes without the log;
// failing that, it is an error.
func (a *app) renderApply(r core.ApplyReport, marker string) (redact.Text, error) {
	limit, err := commentLimit(r.Host)
	if err != nil {
		return redact.Text{}, err
	}
	if r.Host.HiddenHTML { // the marker goes above the body
		limit -= utf8.RuneCountInString(marker) + 1
	}
	r.Budget = limit
	for tries := 0; ; tries++ {
		raw, err := core.RenderApplyReport(r)
		if err != nil {
			return redact.Text{}, err
		}
		body, err := a.filter.Apply(raw)
		if err != nil {
			return body, err
		}
		n := utf8.RuneCountInString(body.String())
		switch {
		case n <= limit:
			return body, nil
		case r.Log == "":
			return body, fmt.Errorf("the filtered apply-result comment is %d characters, over its %d-character budget", n, limit)
		case tries >= 3: // the minimal body: no log
			r.Log = ""
		default:
			// The filter scales the text, so scale the render budget by how
			// far over it came out, with a margin.
			r.Budget = r.Budget*limit/n - 100
		}
	}
}

// mergedPRs are the PRs the repo host says merged this commit; none if it
// can't say.
func (a *app) mergedPRs(v vcs.VCS) []vcs.PR {
	prs, err := v.PRsForCommit(a.ctx.SHA, a.ctx.Ref)
	if err != nil {
		a.ci.Log(ci.Warning, fmt.Sprintf("Could not look up the PR that merged %s: %s", a.ctx.SHA, err))
	}
	return prs
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
		for _, p := range a.mergedPRs(v) {
			g.PRs = append(g.PRs, strconv.Itoa(p.Number))
		}
		if len(g.PRs) == 1 {
			g.PR = g.PRs[0]
		}
	}
	r := core.ApplyReport{Gate: g}
	var incomplete error
	if g.Action == core.GateApply {
		s := os.Getenv("TFPR_APPLY_EXIT_CODE")
		if r.ExitCode, err = strconv.Atoi(s); err != nil {
			// The apply step never set an exit code: it failed before it
			// started (say, its login failed) or was cancelled. Say so on the
			// PR, then fail.
			r.Incomplete = true
			incomplete = fmt.Errorf("the apply did not complete: TFPR_APPLY_EXIT_CODE is '%s', not an exit code", s)
		}
	}
	if path := os.Getenv("TFPR_APPLY_LOG"); path != "" && g.Action == core.GateApply {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			a.ci.Log(ci.Warning, "TFPR_APPLY_LOG "+path+" does not exist; reporting without the apply's output.")
		case err != nil:
			return fmt.Errorf("TFPR_APPLY_LOG: %w", err)
		default:
			text, err := a.filter.Apply(string(data))
			if err != nil {
				return fmt.Errorf("%w; nothing was posted", err)
			}
			r.Log = text.String()
		}
	}
	if err := a.publishApply(v, r); err != nil {
		if errors.Is(err, redact.ErrFailed) || errors.Is(err, vcs.ErrNoAuthor) {
			return fmt.Errorf("%w; nothing was posted", err)
		}
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		a.ci.Log(ci.Warning, "Could not post the apply-result comment.")
	}
	return incomplete
}

// publishApply upserts the apply-result comment on the PR that merged this
// commit, as the gate found it: only when exactly one PR matched.
func (a *app) publishApply(v vcs.VCS, r core.ApplyReport) error {
	sha := a.ctx.SHA
	switch len(r.Gate.PRs) {
	case 0:
		// Not every apply comes from a PR merge (drift, manual dispatch).
		fmt.Fprintf(a.stdout, "No PR associated with %s; skipping apply-result comment.\n", sha)
		return nil
	case 1:
	default:
		a.ci.Log(ci.Warning, fmt.Sprintf("Several PRs match %s (#%s); not posting the apply result, to avoid the wrong PR.",
			sha, strings.Join(r.Gate.PRs, ", #")))
		return nil
	}
	pr := r.Gate.PRs[0]
	if r.Gate.Reason == "superseded" {
		r.SupersededBy = a.ci.RunFor(r.Gate.BranchHead)
	}
	marker := core.ApplyMarker(a.stack, a.workspace)
	r.Stack, r.Workspace, r.SHA, r.JobURL, r.ToolVersion = a.stack, a.workspace, sha, a.ci.JobURL(), version
	r.Host = a.commentHost()
	body, err := a.renderApply(r, marker)
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
