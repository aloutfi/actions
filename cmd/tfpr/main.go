// Command tfpr runs Terraform plan -> PR comment -> apply.
//
// Usage: tfpr <command>
//
//	plan              init, workspace select, validate, plan; write the plan dir
//	                  (plan-result.json, filtered plan text), plus plan.json,
//	                  tfplan and plan-metadata.json in the stack
//	derive            write the plan dir from a plan the caller produced
//	                  (TFPR_PLAN_JSON, TFPR_PLAN_TXT): "bring your own plan"
//	render            print the plan comment body from the plan dir (no CI/VCS calls)
//	comment           write the full plan to the job summary, then render the plan
//	                  comment from the plan dir and upsert it on the current PR; a
//	                  failed or missing plan posts "plan failed". Outside a PR it
//	                  writes only the summary
//	decide            emit whether apply should fetch the planfile artifact
//	apply             plan-source decision, superseded-tree guard, apply,
//	                  apply-result comment on the commit's PR
//	resolve-plan-run  find the plan run for the PR that produced this commit
//
// Configuration comes from TFPR_* environment variables:
//
//	TFPR_STACK                      terraform directory and comment label (terraform/);
//	                                cleaned once, so infra/, ./infra and infra are one stack
//	TFPR_WORKSPACE                  terraform workspace (default)
//	TFPR_TF_BIN                     terraform executable (terraform)
//	TFPR_DIR                        plan dir shared by plan/derive and render/comment
//	                                (<stack>/.tfpr)
//	TFPR_FILTER                     redaction filter, run with sh -c: stdin -> stdout.
//	                                Every terraform output tfpr echoes, the stored plan
//	                                text, names from plan.json, and every comment body
//	                                pass through it; a failure sends nothing
//	TFPR_PLAN_JSON, TFPR_PLAN_TXT   derive: terraform show -json / -no-color output
//	TFPR_EXIT_CODE                  derive: the plan's -detailed-exitcode (optional)
//	TFPR_PLANFILE                   derive: the binary planfile, hashed (optional)
//	TFPR_JOB_URL, TFPR_SUMMARY_URL  render: job log and job summary links (comment
//	                                fills these in)
//	TFPR_PLAN_TXT_URL               render/comment: link to the plan.txt artifact
//	TFPR_COMMENT_BUDGET             render/comment: max characters in the whole comment,
//	                                marker and record included; at most the repo host's
//	                                limit, which is the default (65536 on GitHub), and
//	                                at least the marker and record plus 1000
//	TFPR_TEMPLATE                   render/comment: template file whose {{define}}
//	                                blocks replace the default comment's
//	TFPR_KEEP_UNCHANGED             render/comment: keep "# (N unchanged ... hidden)"
//	                                lines in the comment (false)
//	TFPR_SUMMARY                    comment: write the job summary (true)
//	TFPR_SUMMARY_FILE               comment, outside a supported CI: summary file
//	TFPR_COMMENT_AUTHOR             comment/apply: login allowed to own the comment
//	TFPR_USE_PLANFILE               decide/apply: apply the stored planfile (false)
//	TFPR_ALLOW_FRESH_PLAN_FALLBACK  apply: fresh-plan if the planfile is missing (false)
//	TFPR_PLAN_DIR                   decide/apply: directory the planfile artifact is
//	                                downloaded into; decide empties it first
//	TFPR_PLAN_RUN_ID                apply: run that produced the planfile (comment only)
//	TFPR_LOCK_TIMEOUT               apply: -lock-timeout (5m)
//	TFPR_PLAN_WORKFLOW              resolve-plan-run: plan workflow file name
//	TFPR_VCS                        repo host: github, azure or none (default: where
//	                                the CI says the repo lives, else github)
//	TFPR_REPO                       repo id for the repo host (the CI's repository):
//	                                owner/repo, or <project>/<repository> on Azure Repos
//	TFPR_ACK                        Azure Repos: keep a reviewer's resolution of a plan
//	                                thread until the plan changes (false)
//	TFPR_OUTPUT_FILE                step-output file outside a supported CI (stdout)
//
// The GitHub adapters share one client: TFPR_GITHUB_TOKEN (outside the
// actions: GH_TOKEN or GITHUB_TOKEN) against TFPR_GITHUB_API_URL, else the
// CI's GitHub API (GITHUB_API_URL on GitHub Actions), else
// https://api.github.com. Azure Repos uses TFPR_ADO_TOKEN (map
// $(System.AccessToken) to it) against the build's collection. terraform never sees any TFPR_* variable, so the
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
	"github.com/aloutfi/actions/redact"
	"github.com/aloutfi/actions/vcs"
)

// version is set at release build time (-X main.version=...).
var version = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// exitCode ends a command with a status that has already been explained,
// such as terraform's own exit code.
type exitCode int

func (e exitCode) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// tfFailed is a terraform command that exited non-zero. tfpr exits with the
// same status, and its message is the reason a failed plan records.
type tfFailed struct {
	cmd  string
	code int
}

func (e tfFailed) Error() string { return fmt.Sprintf("terraform %s exited %d", e.cmd, e.code) }
func (e tfFailed) Unwrap() error { return exitCode(e.code) }

// asTFFailed names a terraform exit status after the command that produced it.
func asTFFailed(cmd string, err error) error {
	var code exitCode
	if errors.As(err, &code) {
		return tfFailed{cmd, int(code)}
	}
	return err
}

type app struct {
	stdout, stderr   io.Writer
	stack, workspace string
	api              *vcs.Client // the one GitHub client
	apiErr           error       // why its base URL is unknown
	filter           redact.Filter
	tfBin            string // resolved terraform executable
	tfErr            error  // why tfBin could not be resolved
	ci               ci.CI  // nil until loadCI
	streamErr        error  // the first filter failure on terraform's echoed output
	ctx              ci.Ctx
}

func run(args []string, stdout, stderr io.Writer) int {
	a := &app{
		stdout:    stdout,
		stderr:    stderr,
		stack:     core.CleanStack(envOr("TFPR_STACK", "terraform/")),
		workspace: envOr("TFPR_WORKSPACE", "default"),
	}
	a.filter = redact.Configured(stderr, a.logIssue)
	a.tfBin, a.tfErr = resolveExecutable(envOr("TFPR_TF_BIN", "terraform"))
	commands := map[string]func() error{
		"plan":             a.plan,
		"derive":           a.derive,
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
		return a.exit(errors.New("usage: tfpr plan|derive|render|comment|decide|apply|resolve-plan-run"))
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

// loadCI detects the CI and builds the one GitHub REST client that both the
// CI adapter (job links) and the repo-host adapter use. Its base URL is
// settled once the CI says where GitHub is.
func (a *app) loadCI() {
	a.api = &vcs.Client{}
	a.ci = ci.Detect(os.Getenv, a.stdout, a.stderr, a.api)
	a.ctx = a.ci.Context()
	var base string
	base, a.apiErr = githubAPIURL(a.ctx) // on error base is "": every request fails
	*a.api = *vcs.NewGitHubClient(base, githubToken())
}

// githubAPIURL is the GitHub API to use: TFPR_GITHUB_API_URL, else what the
// CI says (GITHUB_API_URL on GitHub Actions, the repository's host for
// GitHub Enterprise on Azure Pipelines), else the public API, but never the
// public API for a private host.
func githubAPIURL(ctx ci.Ctx) (string, error) {
	if u := os.Getenv("TFPR_GITHUB_API_URL"); u != "" {
		return u, nil
	}
	if ctx.VCS == "github" && ctx.VCSURL != "" {
		return ctx.VCSURL, nil
	}
	if ctx.VCSPrivate {
		return "", errors.New("the repository is on GitHub Enterprise, but its API URL is unknown: set TFPR_GITHUB_API_URL (https://<host>/api/v3); tfpr will not send its token to api.github.com")
	}
	return vcs.GitHubAPI, nil
}

// loadVCS picks the repo host: TFPR_VCS, else the one the CI says the repo
// lives on (Azure Pipelines can build Azure Repos or GitHub repos), else
// GitHub. Each host gets only its own token.
func (a *app) loadVCS() (vcs.VCS, error) {
	cfg := vcs.Config{
		Name:   envOr("TFPR_VCS", a.ctx.VCS),
		Repo:   a.repo(),
		Author: os.Getenv("TFPR_COMMENT_AUTHOR"),
		Ack:    os.Getenv("TFPR_ACK") == "true",
	}
	// GitHub Actions' token posts as github-actions[bot]. Anywhere else the
	// GitHub adapter asks GitHub whose token it holds.
	if cfg.Author == "" && a.ctx.CI == "github" {
		cfg.Author = vcs.DefaultGitHubAuthor
	}
	if cfg.Name == "azure" {
		cfg.URL, cfg.Token = a.ctx.VCSURL, os.Getenv("TFPR_ADO_TOKEN")
	} else {
		if a.apiErr != nil && cfg.Name != "none" {
			return nil, a.apiErr
		}
		cfg.GitHub = a.api
	}
	return vcs.New(cfg)
}

func (a *app) repo() string { return envOr("TFPR_REPO", a.ctx.Repo) }

// logIssue logs a warning or error re-emitted from filtered text, through
// the CI once it is loaded.
func (a *app) logIssue(level, msg string) {
	if a.ci != nil {
		a.ci.Log(level, msg)
		return
	}
	fmt.Fprintf(a.stderr, "tfpr: %s: %s\n", level, msg)
}

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
	cmd.Env = redact.WithoutTFPR(os.Environ())
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return exitCode(ee.ExitCode())
	}
	return err
}

// tf runs terraform in dir with its output echoed through the filter, in a
// region where the CI interprets no commands. It returns terraform's own
// result, unchanged. A filter failure on the echoed output is kept in
// a.streamErr; the command reports it separately and fails, but never
// changes terraform's exit code.
func (a *app) tf(dir string, args ...string) error {
	iout, ierr := a.ci.Inert(a.stdout), a.ci.Inert(a.stderr)
	out, errOut := a.filter.Writer(iout), a.filter.Writer(ierr)
	err := a.terraform(dir, out, errOut, args...)
	a.noteStream(closeAll(out, errOut, iout, ierr))
	return err
}

// tfOutput runs terraform in dir and returns its stdout unfiltered; its
// stderr is echoed like tf's.
func (a *app) tfOutput(dir string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	ierr := a.ci.Inert(a.stderr)
	errOut := a.filter.Writer(ierr)
	err := a.terraform(dir, &out, errOut, args...)
	a.noteStream(closeAll(errOut, ierr))
	return out.Bytes(), err
}

func (a *app) noteStream(err error) {
	if err != nil && a.streamErr == nil {
		a.streamErr = fmt.Errorf("%w (on terraform's output)", err)
	}
}

// closeAll closes ws in order (filters before the inert regions they write
// into) and returns the first failure.
func closeAll(ws ...io.Closer) error {
	var first error
	for _, w := range ws {
		if err := w.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
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

// plan runs terraform and writes the plan dir. If anything fails, the plan
// dir still gets a PlanResult with status error and the reason, so the
// comment step (which runs even when plan fails) replaces the last good plan.
func (a *app) plan() (err error) {
	a.loadCI()
	tool := core.Tool{Runner: "terraform", Binary: filepath.Base(a.tfBin)}
	var planCode *int // terraform plan's exit code, once it ran
	defer func() {
		if err != nil {
			a.recordFailure(tool, a.filter.Mode(), planCode, err)
		}
	}()
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
			return asTFFailed(args[0], err)
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
	planCode = &code
	if err := a.outputs(core.Field{Name: "exit_code", Value: strconv.Itoa(code)}); err != nil {
		return err
	}
	if code == 1 {
		return tfFailed{"plan", 1}
	}
	// The output above reports terraform's code as it was; a failed filter on
	// its echoed output still fails the plan.
	if a.streamErr != nil {
		return a.streamErr
	}

	// plan.json stays in the stack, next to tfplan; it holds values in
	// clear text and is read only for structure.
	planJSON, err := a.tfOutput(dir, "show", "-json", "tfplan")
	err = asTFFailed("show", err)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "plan.json"), planJSON, 0o644)
	}
	if err != nil {
		return err
	}
	planTxt, err := a.tfOutput(dir, "show", "-no-color", "tfplan")
	if err != nil {
		return asTFFailed("show", err)
	}
	text, err := a.filter.Apply(string(planTxt))
	if err != nil {
		return err
	}
	planfileSHA, err := fileSHA256(filepath.Join(dir, "tfplan"))
	if err != nil {
		return err
	}
	unit, format, err := a.deriveUnit(core.UnitInput{Path: ".", PlanJSON: planJSON, ExitCode: &code, PlanfileSHA256: planfileSHA}, text)
	if err != nil {
		return err
	}
	tool.BinaryVersion, tool.PlanFormatVersion = a.terraformVersion(dir), format
	r, err := a.writePlanDir(unit, &text, tool, a.filter.Mode())
	if err != nil {
		return err
	}
	stats := core.StatsOf(r.Summary.Counts)
	fields := append(stats.Fields(),
		core.Field{Name: "plan_artifact_name", Value: core.ArtifactName(a.stack, a.workspace)},
		core.Field{Name: "plan_text", Value: filepath.Join(a.planDir(), "units", filepath.FromSlash(unit.Path), "plan.txt")})
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
		a.stack, a.workspace, r.Tool.BinaryVersion, stats.HasChanges(), stats.HasImports()}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "plan-metadata.json"), buf.Bytes(), 0o644)
}

// render prints the plan comment from the plan dir. Its stdout is data
// (tfpr render > comment.md), so the body is written as is: it has already
// been filtered, and CI command lines have been stripped from it.
func (a *app) render() error {
	a.loadCI()
	r, texts, err := a.readPlanDir()
	if err != nil {
		return err
	}
	links := core.Links{Job: os.Getenv("TFPR_JOB_URL"), Summary: os.Getenv("TFPR_SUMMARY_URL"), PlanText: os.Getenv("TFPR_PLAN_TXT_URL")}
	body, marker, record, err := a.renderPlan(r, texts, links)
	if err != nil {
		return err
	}
	_, err = io.WriteString(a.stdout, vcs.Compose(marker, record, body))
	return err
}

// comment posts the plan comment from the plan dir. A failed plan posts a
// "plan failed" body over the stack's comment. So does a missing plan dir:
// comment records the failure itself, posts it, and exits non-zero.
func (a *app) comment() error {
	a.loadCI()
	v, err := a.loadVCS()
	if err != nil {
		return err
	}
	pr := a.ctx.PR
	r, texts, err := a.readPlanDir()
	links := core.Links{PlanText: os.Getenv("TFPR_PLAN_TXT_URL")}
	if pr == "" {
		// No comment, but the job summary still gets the full plan.
		if err == nil {
			if _, err := a.writeSummary(r, texts, links); err != nil {
				return fmt.Errorf("%w; nothing was published", err)
			}
		}
		fmt.Fprintln(a.stdout, "Not a pull request build; skipping plan comment.")
		return nil
	}
	missing := errors.Is(err, errNoPlanResult)
	if missing {
		r, texts, err = a.recordFailure(core.Tool{Runner: "byo"}, a.filter.Mode(), nil, err), nil, nil
	}
	if err != nil {
		return err
	}
	links.Job = a.ci.JobURL()
	written, err := a.writeSummary(r, texts, links)
	if written {
		links.Summary = a.summaryURL()
	}
	if err == nil {
		var body redact.Text
		var marker, record string
		body, marker, record, err = a.renderPlan(r, texts, links)
		if err == nil {
			return a.postPlanComment(v, pr, r, marker, record, body, missing)
		}
	}
	if errors.Is(err, redact.ErrFailed) {
		return fmt.Errorf("%w; nothing was posted", err)
	}
	return err
}

// postPlanComment upserts the rendered plan comment.
func (a *app) postPlanComment(v vcs.VCS, pr string, r core.PlanResult, marker, record string, body redact.Text, missing bool) error {
	// Best effort: a comment API failure shouldn't hide the plan's own result.
	msg, err := v.UpsertComment(vcs.Comment{
		PR: pr, Marker: marker, Record: record, Body: body,
		Resolved: r.Summary.Status == core.StatusNoOp, Fingerprint: core.RunFingerprint(r),
	})
	switch {
	case errors.Is(err, vcs.ErrNoAuthor):
		return err // a setup error, not a flaky API: fix it, don't skip it
	case err != nil && r.Summary.Status == core.StatusError:
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		a.ci.Log(ci.Warning, "Could not post the plan-failed comment on PR #"+pr+".")
	case err != nil:
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		a.ci.Log(ci.Warning, "Could not post the plan comment on PR #"+pr+"; the plan itself succeeded.")
	default:
		fmt.Fprintln(a.stdout, msg)
	}
	if missing {
		return fmt.Errorf("%s: no plan-result.json; posted a plan-failed comment", a.planDir())
	}
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
// whose plan-metadata.json is for this stack and workspace. A tfplan already
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
	// Cleaned here too: metadata written before stacks were normalized may
	// still carry "infra/".
	stack, ok := meta["stack"].(string)
	if err != nil || !ok || core.CleanStack(stack) != a.stack || meta["workspace"] != a.workspace {
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
	if t := core.DescribeDecision(d, attempt, core.ArtifactName(a.stack, a.workspace), ""); t.Log != "" {
		a.log(t.Level, t.Log)
	}
	code := 0
	if d.Source == core.SourceNone {
		code = 1
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
	// A filter failure fails the step, but code stays terraform's: it is what
	// the outputs and the comment report.
	stepFailed := false
	if err := a.postApplyComment(v, d, code, used, usePlanfile, attempt); errors.Is(err, redact.ErrFailed) || errors.Is(err, vcs.ErrNoAuthor) {
		a.ci.Log(ci.Error, err.Error()+"; nothing was posted")
		stepFailed = true
	} else if err != nil {
		fmt.Fprintf(a.stderr, "tfpr: %s\n", err)
		a.ci.Log(ci.Warning, fmt.Sprintf("Could not post the apply-result comment; the apply exit code (%d) stands.", code))
	}
	if a.streamErr != nil {
		a.ci.Log(ci.Error, a.streamErr.Error())
		stepFailed = true
	}
	switch {
	case code != 0:
		return exitCode(code)
	case stepFailed:
		return exitCode(1)
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
	if usePlanfile && core.IsFirstAttempt(attempt) {
		artifact = core.ArtifactName(a.stack, a.workspace)
	}
	raw, err := core.RenderApply(core.ApplyComment{
		Stack: a.stack, Workspace: a.workspace,
		Decision: d, ExitCode: code, UsedPlanfile: used,
		Artifact: artifact, PlanRun: os.Getenv("TFPR_PLAN_RUN_ID"),
		RunID: a.ctx.RunID, RunURL: a.ctx.RunURL, SHA: sha,
		TerraformVersion: a.terraformVersion(""),
	})
	if err != nil {
		return err
	}
	body, err := a.filter.Apply(raw)
	if err != nil {
		return err
	}
	// A failed apply needs attention: its thread stays active.
	msg, err := v.UpsertComment(vcs.Comment{PR: pr, Marker: core.ApplyMarker(a.stack, a.workspace), Body: body, Resolved: code == 0})
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

	if !core.IsFirstAttempt(attempt) {
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
