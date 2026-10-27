// Command tfpr runs Terraform plan -> PR comment -> apply.
//
// Usage: tfpr <command>
//
//	plan              init, workspace select, validate, plan; parse counts;
//	                  write plan.json, plan.txt, plan-metadata.json
//	render            print the plan comment body to stdout (no CI/VCS calls)
//	comment           render the plan comment and upsert it on the current PR
//	decide            emit whether apply should fetch the planfile artifact
//	apply             plan-source decision, superseded-tree guard, apply,
//	                  apply-result comment on the commit's PR
//	resolve-plan-run  find the plan run for the PR that produced this commit
//
// Configuration comes from TFPR_* environment variables:
//
//	TFPR_STACK                      terraform directory and comment label (terraform/)
//	TFPR_WORKSPACE                  terraform workspace (default)
//	TFPR_TF_BIN                     terraform executable (terraform)
//	TFPR_EXIT_CODE                  render/comment: the plan's -detailed-exitcode
//	TFPR_RUN_ID, TFPR_JOB_URL       render: run link (comment fills these in)
//	TFPR_COMMENT_BUDGET             render: max plan.txt bytes in the comment (45000)
//	TFPR_COMMENT_AUTHOR             comment/apply: login allowed to own the comment
//	TFPR_USE_PLANFILE               decide/apply: apply the stored planfile (false)
//	TFPR_ALLOW_FRESH_PLAN_FALLBACK  apply: fresh-plan if the planfile is missing (false)
//	TFPR_PLAN_DIR                   decide/apply: directory the planfile artifact is
//	                                downloaded into; decide empties it first
//	TFPR_PLAN_RUN_ID                apply: run that produced the planfile (comment only)
//	TFPR_LOCK_TIMEOUT               apply: -lock-timeout (5m)
//	TFPR_PLAN_WORKFLOW              resolve-plan-run: plan workflow file name
//	TFPR_VCS                        repo host: github (default), or none
//	TFPR_REPO                       repo id for the repo host (the CI's repository)
//	TFPR_OUTPUT_FILE                step-output file outside a supported CI (stdout)
//
// The GitHub adapters authenticate with TFPR_GITHUB_TOKEN (outside the
// actions: GH_TOKEN or GITHUB_TOKEN) against GITHUB_API_URL
// (https://api.github.com). terraform never sees any TFPR_* variable, so the
// token the actions pass stays out of it; a GITHUB_TOKEN the caller sets for
// the GitHub provider still reaches it.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aloutfi/actions/ci"
	"github.com/aloutfi/actions/core"
	"github.com/aloutfi/actions/vcs"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// exitCode ends a command with a status that has already been explained,
// such as terraform's own exit code.
type exitCode int

func (e exitCode) Error() string { return "exit status " + strconv.Itoa(int(e)) }

type app struct {
	stdout, stderr   io.Writer
	stack, workspace string
	api              *vcs.Client
	tfBin            string // resolved terraform executable
	tfErr            error  // why tfBin could not be resolved
	ci               ci.CI  // nil until loadCI
	ctx              ci.Ctx
}

func run(args []string, stdout, stderr io.Writer) int {
	a := &app{
		stdout:    stdout,
		stderr:    stderr,
		stack:     envOr("TFPR_STACK", "terraform/"),
		workspace: envOr("TFPR_WORKSPACE", "default"),
		api:       vcs.NewGitHubClient(os.Getenv("GITHUB_API_URL"), githubToken()),
	}
	a.tfBin, a.tfErr = resolveExecutable(envOr("TFPR_TF_BIN", "terraform"))
	commands := map[string]func() error{
		"plan":             a.plan,
		"render":           a.render,
		"comment":          a.comment,
		"decide":           a.decide,
		"apply":            a.apply,
		"resolve-plan-run": a.resolvePlanRun,
	}
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	f, ok := commands[cmd]
	if !ok {
		return a.exit(errors.New("usage: tfpr plan|render|comment|decide|apply|resolve-plan-run"))
	}
	return a.exit(f())
}

// exit reports err (through the CI once it is loaded) and returns the
// process exit status.
func (a *app) exit(err error) int {
	var code exitCode
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	case a.ci != nil:
		a.ci.Log(ci.Error, err.Error())
	default:
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
	}
	return 1
}

func (a *app) loadCI() {
	a.ci = ci.Detect(os.Getenv, a.stdout, a.stderr, a.api)
	a.ctx = a.ci.Context()
}

func (a *app) loadVCS() (vcs.VCS, error) {
	return vcs.New(vcs.Config{
		Name:   os.Getenv("TFPR_VCS"),
		Repo:   a.repo(),
		API:    a.api,
		Author: os.Getenv("TFPR_COMMENT_AUTHOR"),
	})
}

func (a *app) repo() string { return envOr("TFPR_REPO", a.ctx.Repo) }

// log prints msg at a CI log level, or as plain output when level is "".
func (a *app) log(level, msg string) {
	if level == "" {
		fmt.Fprintln(a.stdout, msg)
		return
	}
	a.ci.Log(level, msg)
}

func (a *app) outputs(fields ...core.Field) error {
	for _, f := range fields {
		if err := a.ci.SetOutput(f.Name, f.Value); err != nil {
			return fmt.Errorf("set output %s: %w", f.Name, err)
		}
	}
	return nil
}

// terraform runs the terraform executable ($TFPR_TF_BIN, default terraform)
// in dir, with tfpr's own TFPR_* variables (including the API token) removed
// from its environment. Every terraform call goes through here. A non-zero
// exit becomes an exitCode carrying terraform's status.
func (a *app) terraform(dir string, stdout, stderr io.Writer, args ...string) error {
	if a.tfErr != nil {
		return a.tfErr
	}
	cmd := exec.Command(a.tfBin, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, stdout, stderr
	cmd.Env = terraformEnv(os.Environ())
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return exitCode(ee.ExitCode())
	}
	return err
}

// tf runs terraform in dir with its output passed through.
func (a *app) tf(dir string, args ...string) error {
	return a.terraform(dir, a.stdout, a.stderr, args...)
}

// tfOutput runs terraform in dir and returns its stdout.
func (a *app) tfOutput(dir string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := a.terraform(dir, &out, a.stderr, args...)
	return out.Bytes(), err
}

func (a *app) terraformVersion(dir string) string {
	var out bytes.Buffer
	if a.terraform(dir, &out, io.Discard, "version", "-json") != nil {
		return "unknown"
	}
	var v struct {
		Version string `json:"terraform_version"`
	}
	if json.Unmarshal(out.Bytes(), &v) != nil || v.Version == "" {
		return "unknown"
	}
	return v.Version
}

func (a *app) plan() error {
	a.loadCI()
	dir := a.stack
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("stack directory '%s' not found", dir)
	}
	for _, args := range [][]string{
		{"init", "-input=false"},
		{"workspace", "select", "-or-create", a.workspace},
		{"validate", "-no-color"},
	} {
		if err := a.tf(dir, args...); err != nil {
			return err
		}
	}
	// -detailed-exitcode: 0 = no changes, 1 = error, 2 = changes. Only 1 fails.
	code := 0
	if err := a.tf(dir, "plan", "-input=false", "-no-color", "-detailed-exitcode", "-out=tfplan"); err != nil {
		var c exitCode
		if !errors.As(err, &c) {
			return err
		}
		code = int(c)
	}
	if err := a.outputs(core.Field{Name: "exit_code", Value: strconv.Itoa(code)}); err != nil {
		return err
	}
	if code == 1 {
		return exitCode(1)
	}

	planJSON, err := a.tfOutput(dir, "show", "-json", "tfplan")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "plan.json"), planJSON, 0o644)
	}
	if err != nil {
		return err
	}
	planTxt, err := a.tfOutput(dir, "show", "-no-color", "tfplan")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "plan.txt"), planTxt, 0o644)
	}
	if err != nil {
		return err
	}
	stats, err := core.PlanStats(planJSON)
	if err != nil {
		return err
	}
	fields := append(stats.Fields(), core.Field{Name: "plan_artifact_name", Value: core.ArtifactName(a.stack, a.workspace)})
	if err := a.outputs(fields...); err != nil {
		return err
	}

	meta := struct {
		SHA              string `json:"sha"`
		PRNumber         string `json:"pr_number"`
		Actor            string `json:"actor"`
		CreatedAt        string `json:"created_at"`
		Stack            string `json:"stack"`
		Workspace        string `json:"workspace"`
		TerraformVersion string `json:"terraform_version"`
		HasChanges       bool   `json:"has_changes"`
		HasImports       bool   `json:"has_imports"`
	}{a.ctx.SHA, a.ctx.PR, a.ctx.Actor, time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		a.stack, a.workspace, a.terraformVersion(dir), stats.HasChanges(), stats.HasImports()}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "plan-metadata.json"), buf.Bytes(), 0o644)
}

func (a *app) render() error {
	body, err := a.renderPlan(os.Getenv("TFPR_RUN_ID"), os.Getenv("TFPR_JOB_URL"))
	if err != nil {
		return err
	}
	_, err = io.WriteString(a.stdout, body)
	return err
}

func (a *app) renderPlan(runID, jobURL string) (string, error) {
	exit := os.Getenv("TFPR_EXIT_CODE")
	if exit == "" {
		return "", errors.New("TFPR_EXIT_CODE is required")
	}
	b := envOr("TFPR_COMMENT_BUDGET", "45000")
	budget, err := strconv.Atoi(b)
	if err != nil || budget < 0 {
		return "", fmt.Errorf("TFPR_COMMENT_BUDGET must be a byte count, got '%s'", b)
	}
	planJSON, err := os.ReadFile(filepath.Join(a.stack, "plan.json"))
	if err != nil {
		return "", err
	}
	stats, err := core.PlanStats(planJSON)
	if err != nil {
		return "", err
	}
	planTxt, err := os.ReadFile(filepath.Join(a.stack, "plan.txt"))
	if err != nil {
		return "", err
	}
	return core.RenderPlan(core.PlanComment{
		Stack: a.stack, Workspace: a.workspace, ExitCode: exit,
		RunID: runID, JobURL: jobURL,
		Stats: stats, PlanText: planTxt, Budget: budget,
	})
}

func (a *app) comment() error {
	a.loadCI()
	v, err := a.loadVCS()
	if err != nil {
		return err
	}
	pr := a.ctx.PR
	if pr == "" {
		fmt.Fprintln(a.stdout, "Not a pull request build; skipping plan comment.")
		return nil
	}
	body, err := a.renderPlan(a.ctx.RunID, a.ci.JobURL())
	if err != nil {
		return err
	}
	// Best effort: the plan succeeded, and a comment API failure shouldn't hide that.
	msg, err := v.UpsertComment(pr, core.PlanMarker(a.stack, a.workspace), body)
	if err != nil {
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		a.ci.Log(ci.Warning, "Could not post the plan comment on PR #"+pr+"; the plan itself succeeded.")
		return nil
	}
	fmt.Fprintln(a.stdout, msg)
	return nil
}

func (a *app) decide() error {
	a.loadCI()
	// Start empty so apply only ever sees what this download put there.
	if dir := os.Getenv("TFPR_PLAN_DIR"); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	d := core.DecidePlanSource(os.Getenv("TFPR_USE_PLANFILE") == "true", a.ctx.Attempt, true, false)
	return a.outputs(
		core.Field{Name: "download", Value: strconv.FormatBool(d.Source == core.SourcePlanfile)},
		core.Field{Name: "artifact_name", Value: core.ArtifactName(a.stack, a.workspace)},
	)
}

// reviewedPlanfile reports whether TFPR_PLAN_DIR holds a downloaded planfile
// whose plan-metadata.json is for this stack (compared as cleaned paths, so
// "infra/" matches "infra") and workspace. A tfplan already
// in the stack directory (say, from an earlier plan in the same job) never
// counts as the reviewed plan.
func (a *app) reviewedPlanfile() bool {
	dir := os.Getenv("TFPR_PLAN_DIR")
	if dir == "" {
		return false
	}
	if fi, err := os.Stat(filepath.Join(dir, "tfplan")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	var meta map[string]any
	data, err := os.ReadFile(filepath.Join(dir, "plan-metadata.json"))
	if err == nil {
		err = json.Unmarshal(data, &meta)
	}
	stack, ok := meta["stack"].(string)
	if err != nil || !ok || !core.SameStack(stack, a.stack) || meta["workspace"] != a.workspace {
		a.ci.Log(ci.Warning, fmt.Sprintf("Ignoring the planfile artifact: its plan-metadata.json is missing or is not for stack '%s' workspace '%s'.", a.stack, a.workspace))
		return false
	}
	return true
}

func (a *app) apply() error {
	a.loadCI()
	v, err := a.loadVCS()
	if err != nil {
		return err
	}
	usePlanfile := os.Getenv("TFPR_USE_PLANFILE") == "true"
	allow := os.Getenv("TFPR_ALLOW_FRESH_PLAN_FALLBACK") == "true"
	attempt := a.ctx.Attempt

	d := core.DecidePlanSource(usePlanfile, attempt, a.reviewedPlanfile(), allow)
	code := 0
	switch d {
	case core.Decision{Source: core.SourceFresh, Reason: "rerun"}:
		a.ci.Log(ci.Warning, fmt.Sprintf("Re-run detected (attempt=%d); skipping stored planfile to avoid stale-plan apply", attempt))
	case core.Decision{Source: core.SourceFresh, Reason: "missing-artifact"}:
		a.ci.Log(ci.Warning, "Planfile artifact requested but not found; falling back to fresh-plan apply (allow_fresh_plan_fallback=true)")
	case core.Decision{Source: core.SourceNone, Reason: "missing-artifact"}:
		a.ci.Log(ci.Error, fmt.Sprintf("Planfile artifact '%s' not found; refusing to apply a fresh, unreviewed plan. It may have expired (the plan action's retention_days input sets how long it is kept) or the plan run did not upload it. Re-run the plan, or set allow_fresh_plan_fallback=true to apply a fresh plan instead.", core.ArtifactName(a.stack, a.workspace)))
		code = 1
	case core.Decision{Source: core.SourcePlanfile}:
		fmt.Fprintln(a.stdout, "Found planfile from artifact; will apply it")
	}

	if d.Source == core.SourceFresh {
		branchHead := func(branch string) (string, error) {
			head, err := v.BranchHead(branch)
			if err != nil {
				fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
			}
			return head, err
		}
		g := core.SupersededGuard(a.ctx.Ref, a.ctx.SHA, attempt, branchHead)
		a.log(g.Level, g.Message)
		switch g.Code {
		case core.GuardSuperseded:
			d, code = core.Decision{Source: core.SourceNone, Reason: "superseded"}, 1
		case core.GuardUnverified:
			d, code = core.Decision{Source: core.SourceNone, Reason: "unverified-ref"}, 1
		}
	}
	// A partial-apply failure must propagate so downstream jobs see it.
	if d.Source != core.SourceNone {
		code = a.terraformApply(d.Source)
	}

	used := d.Source == core.SourcePlanfile
	if err := a.outputs(
		core.Field{Name: "apply_exit_code", Value: strconv.Itoa(code)},
		core.Field{Name: "used_plan_artifact", Value: strconv.FormatBool(used)},
		core.Field{Name: "fallback_reason", Value: d.Reason},
	); err != nil {
		return err
	}
	// Best effort: a comment API failure must not change the apply's result.
	if err := a.postApplyComment(v, d, code, used, usePlanfile, attempt); err != nil {
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		a.ci.Log(ci.Warning, fmt.Sprintf("Could not post the apply-result comment; the apply exit code (%d) stands.", code))
	}
	if code != 0 {
		return exitCode(code)
	}
	return nil
}

// terraformApply applies the reviewed planfile or a fresh plan and returns
// the exit status of the first step that fails.
func (a *app) terraformApply(source string) int {
	args := []string{"apply", "-input=false", "-no-color", "-lock-timeout=" + envOr("TFPR_LOCK_TIMEOUT", "5m")}
	if source == core.SourcePlanfile {
		// The reviewed planfile encodes intent; no -auto-approve needed. Copy it
		// over whatever tfplan the stack directory holds.
		if err := copyFile(filepath.Join(os.Getenv("TFPR_PLAN_DIR"), "tfplan"), filepath.Join(a.stack, "tfplan")); err != nil {
			fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
			return 1
		}
		args = append(args, "tfplan")
	} else {
		args = append(args, "-auto-approve")
	}
	for _, step := range [][]string{
		{"init", "-input=false"},
		{"workspace", "select", "-or-create", a.workspace},
		args,
	} {
		err := a.tf(a.stack, step...)
		var code exitCode
		switch {
		case errors.As(err, &code):
			return int(code)
		case err != nil:
			fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
			return 1
		}
	}
	return 0
}

func (a *app) postApplyComment(v vcs.VCS, d core.Decision, code int, used, usePlanfile bool, attempt int) error {
	sha := a.ctx.SHA
	// Not every apply comes from a PR merge (drift, manual dispatch); skip quietly.
	prs, err := v.PRsForCommit(sha)
	if err != nil || len(prs) == 0 {
		fmt.Fprintf(a.stdout, "No PR associated with %s; skipping apply-result comment.\n", sha)
		return nil
	}
	pr := strconv.Itoa(prs[0].Number)
	artifact := ""
	if usePlanfile && attempt <= 1 {
		artifact = core.ArtifactName(a.stack, a.workspace)
	}
	body, err := core.RenderApply(core.ApplyComment{
		Stack: a.stack, Workspace: a.workspace,
		Decision: d, ExitCode: code, UsedPlanfile: used,
		Artifact: artifact, PlanRun: os.Getenv("TFPR_PLAN_RUN_ID"),
		RunID: a.ctx.RunID, RunURL: a.ctx.RunURL, SHA: sha,
		TerraformVersion: a.terraformVersion(""),
	})
	if err != nil {
		return err
	}
	msg, err := v.UpsertComment(pr, core.ApplyMarker(a.stack, a.workspace), body)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, msg)
	return nil
}

func (a *app) resolvePlanRun() error {
	a.loadCI()
	gh, ok := a.ci.(*ci.GitHub)
	if !ok {
		return errors.New("resolve-plan-run needs GitHub Actions (GITHUB_ACTIONS=true)")
	}
	v, err := a.loadVCS()
	if err != nil {
		return err
	}
	workflow := os.Getenv("TFPR_PLAN_WORKFLOW")
	if workflow == "" {
		return errors.New("TFPR_PLAN_WORKFLOW is required")
	}
	attempt, sha := a.ctx.Attempt, a.ctx.SHA
	var prNum, runID string

	if attempt > 1 {
		// Re-run: the stored plan reflects state before the failed apply. Emit
		// nothing so apply takes the fresh-plan path.
		a.ci.Log(ci.Notice, fmt.Sprintf("Re-run detected (attempt=%d); fresh-plan fallback", attempt))
	} else {
		prs, err := v.PRsForCommit(sha)
		if err != nil {
			return err
		}
		switch len(prs) {
		case 0:
			a.ci.Log(ci.Notice, fmt.Sprintf("No PR associated with %s; direct-push or hotfix path; fresh-plan fallback", sha))
		case 1:
			head := prs[0].HeadSHA
			prNum = strconv.Itoa(prs[0].Number)
			if runID, err = gh.FindPlanRun(a.repo(), workflow, head); err != nil {
				return err
			}
			if runID == "" {
				a.ci.Log(ci.Notice, fmt.Sprintf("No successful '%s' run found for head_sha=%s (PR #%s); fresh-plan fallback", workflow, head, prNum))
			} else {
				fmt.Fprintf(a.stdout, "Resolved PR #%s head=%s → plan run=%s\n", prNum, head, runID)
			}
		default:
			// Multiple PRs (merge queue, cherry-pick): don't guess.
			a.ci.Log(ci.Warning, fmt.Sprintf("Multiple PRs match %s (count=%d); fresh-plan fallback to avoid ambiguity", sha, len(prs)))
		}
	}
	return a.outputs(core.Field{Name: "run_id", Value: runID}, core.Field{Name: "pr_number", Value: prNum})
}

// resolveExecutable turns name into a path that does not depend on the
// directory terraform runs in: a bare name is looked up on PATH, and a path is
// made absolute against tfpr's working directory.
func resolveExecutable(name string) (string, error) {
	if !strings.ContainsAny(name, `/\`) {
		return exec.LookPath(name)
	}
	return filepath.Abs(name)
}

// terraformEnv is env without any TFPR_* variable.
func terraformEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TFPR_") {
			out = append(out, kv)
		}
	}
	return out
}

// githubToken is the token for GitHub API calls: TFPR_GITHUB_TOKEN, which the
// actions set from their token input, else GH_TOKEN or GITHUB_TOKEN.
func githubToken() string {
	for _, name := range []string{"TFPR_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// envOr is the environment variable name, or def when it is unset or empty.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
