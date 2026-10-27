package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aloutfi/actions/ci"
	"github.com/aloutfi/actions/core"
	"github.com/aloutfi/actions/redact"
)

// The plan dir ($TFPR_DIR, default <stack>/.tfpr) is what plan or derive
// hand to render and comment, possibly in another step or job:
//
//	plan-result.json        the PlanResult (no attribute values)
//	units/<path>/plan.txt   each unit's plan text, already filtered
//
// plan.json is deliberately not in it. It carries sensitive values in clear
// text, and the plan dir is meant to cross credential boundaries as an
// artifact.
func (a *app) planDir() string { return envOr("TFPR_DIR", filepath.Join(a.stack, ".tfpr")) }

// errNoPlanResult means the plan dir has no plan-result.json.
var errNoPlanResult = errors.New("no plan-result.json")

// derive builds the plan dir from a plan the caller produced ("bring your own
// plan"): TFPR_PLAN_JSON and TFPR_PLAN_TXT name the outputs of
// `terraform show -json` and `terraform show -no-color`. TFPR_EXIT_CODE
// (the plan's -detailed-exitcode) and TFPR_PLANFILE (the binary planfile,
// hashed, not copied) are optional.
//
// A failed plan (TFPR_EXIT_CODE=1), missing or invalid inputs, or a filter
// failure still writes a PlanResult, with status error and the reason, so
// the comment replaces the last good plan; derive then exits non-zero.
func (a *app) derive() (err error) {
	a.loadCI()
	var exit *int
	// The mode reflects only TFPR_FILTER: a caller's own scrubber saw
	// plan.txt, never the names tfpr copies from plan.json.
	mode := a.filter.Mode()
	tool := core.Tool{Runner: "byo"}
	defer func() {
		if err != nil {
			a.recordFailure(tool, mode, exit, err)
		}
	}()

	if s := os.Getenv("TFPR_EXIT_CODE"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 2 {
			return fmt.Errorf("TFPR_EXIT_CODE must be 0, 1 or 2 (terraform plan -detailed-exitcode), got '%s'", s)
		}
		exit = &n
	}
	if exit != nil && *exit == 1 {
		return errors.New("the plan failed (TFPR_EXIT_CODE=1)")
	}
	planJSON, err := readInput("TFPR_PLAN_JSON", "terraform show -json")
	if err != nil {
		return err
	}
	planTxt, err := readInput("TFPR_PLAN_TXT", "terraform show -no-color")
	if err != nil {
		return err
	}
	planfileSHA := ""
	if p := os.Getenv("TFPR_PLANFILE"); p != "" {
		if planfileSHA, err = fileSHA256(p); err != nil {
			return err
		}
	}
	text, err := a.filter.Apply(string(planTxt))
	if err != nil {
		return err
	}
	unit, format, err := a.deriveUnit(core.UnitInput{Path: ".", PlanJSON: planJSON, ExitCode: exit, PlanfileSHA256: planfileSHA}, text)
	if err != nil {
		return err
	}
	tool.PlanFormatVersion = format
	r, err := a.writePlanDir(unit, &text, tool, mode)
	if err != nil {
		return err
	}
	stats := core.StatsOf(r.Summary.Counts)
	fields := append([]core.Field{{Name: "exit_code", Value: strconv.Itoa(r.Summary.ExitCode)}}, stats.Fields()...)
	if err := a.outputs(fields...); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "Wrote %s: %s, redaction %s.\n", filepath.Join(a.planDir(), "plan-result.json"), r.Summary.Status, mode)
	return nil
}

// readInput reads the file named by the environment variable env.
func readInput(env, what string) ([]byte, error) {
	path := os.Getenv(env)
	if path == "" {
		return nil, fmt.Errorf("%s is not set: it names the output of `%s`", env, what)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %s does not exist (did the plan run?)", env, path)
	}
	return data, err
}

// deriveUnit derives a unit and passes its names through the filter.
func (a *app) deriveUnit(in core.UnitInput, text redact.Text) (core.Unit, string, error) {
	in.PlanText = text.String()
	unit, format, err := core.DeriveUnit(in)
	if err == nil {
		err = a.redactNames(&unit)
	}
	return unit, format, err
}

// recordFailure writes a PlanResult whose one unit failed for cause, so a
// later comment replaces the last good plan, and returns it. It only warns
// if the write fails: cause is the error that matters.
func (a *app) recordFailure(tool core.Tool, mode string, exit *int, cause error) core.PlanResult {
	var tf tfFailed
	if errors.As(cause, &tf) && tf.cmd == "plan" {
		exit = &tf.code
	}
	r, err := a.writePlanDir(core.FailedUnit(".", exit, cause.Error()), nil, tool, mode)
	if err != nil {
		a.ci.Log(ci.Warning, "Could not record the failed plan in "+a.planDir()+": "+err.Error())
	}
	return r
}

// checkPlanDir refuses to write a plan dir that would create the stack
// directory: a mistyped stack must stay a clear error on every run, not
// become an empty directory.
func (a *app) checkPlanDir() error {
	if _, err := os.Stat(a.stack); err == nil {
		return nil
	}
	rel, err := filepath.Rel(a.stack, a.planDir())
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("stack directory '%s' does not exist, so no plan dir was written inside it", a.stack)
	}
	return nil
}

// writePlanDir writes the plan dir for one unit. text is the unit's filtered
// plan text, nil when the unit failed.
func (a *app) writePlanDir(unit core.Unit, text *redact.Text, tool core.Tool, mode string) (core.PlanResult, error) {
	tool.Version = version
	var pr *string
	if a.ctx.PR != "" {
		pr = &a.ctx.PR
	}
	r := core.NewPlanResult(core.PlanResult{
		Tool:      tool,
		Target:    core.Target{Stack: a.stack, Workspace: a.workspace},
		Source:    core.Source{Repo: a.repo(), SHA: a.ctx.SHA, Tree: gitTree(a.ctx.SHA), HeadSHA: a.ctx.HeadSHA, Ref: a.ctx.Ref, PR: pr},
		Run:       core.Run{CI: a.ctx.CI, ID: a.ctx.RunID, Attempt: a.ctx.Attempt, URL: a.ctx.RunURL},
		Redaction: core.Redaction{Mode: mode},
	}, []core.Unit{unit})

	dir := a.planDir()
	if err := a.checkPlanDir(); err != nil {
		return r, err
	}
	if err := os.RemoveAll(filepath.Join(dir, "units")); err != nil {
		return r, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	if text != nil {
		unitDir := filepath.Join(dir, "units", filepath.FromSlash(unit.Path))
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			return r, err
		}
		if err := os.WriteFile(filepath.Join(unitDir, "plan.txt"), []byte(text.String()), 0o644); err != nil {
			return r, err
		}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return r, err
	}
	return r, os.WriteFile(filepath.Join(dir, "plan-result.json"), append(data, '\n'), 0o644)
}

// redactNames passes the human-readable names taken from plan.json (resource
// addresses, which carry for_each keys, and output names) through the filter,
// in one run with one name per line. A filter that changes the line count
// fails closed.
func (a *app) redactNames(u *core.Unit) error {
	if a.filter.Mode() == redact.ModeNone {
		return nil
	}
	var names []*string
	for i := range u.Resources {
		names = append(names, &u.Resources[i].Address)
		if u.Resources[i].PreviousAddress != "" {
			names = append(names, &u.Resources[i].PreviousAddress)
		}
	}
	for i := range u.Outputs {
		names = append(names, &u.Outputs[i].Name)
	}
	if len(names) == 0 {
		return nil
	}
	lines := make([]string, len(names))
	for i, n := range names {
		lines[i] = *n
	}
	out, err := a.filter.Apply(strings.Join(lines, "\n") + "\n")
	if err != nil {
		return err
	}
	got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(got) != len(names) {
		return fmt.Errorf("%w: it turned %d names into %d lines; filters must keep one line per line", redact.ErrFailed, len(names), len(got))
	}
	for i, n := range names {
		*n = got[i]
	}
	return nil
}

// readPlanDir reads the PlanResult and the plan text of every unit that has
// one, by unit path. Units that failed, did not run or were excluded have none.
func (a *app) readPlanDir() (core.PlanResult, map[string]string, error) {
	dir := a.planDir()
	path := filepath.Join(dir, "plan-result.json")
	var r core.PlanResult
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil, fmt.Errorf("%w in %s: run `tfpr plan` or `tfpr derive` first, or set TFPR_DIR", errNoPlanResult, dir)
	}
	if err != nil {
		return r, nil, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, nil, fmt.Errorf("%s: %w", path, err)
	}
	if r.Schema != core.PlanResultSchema {
		return r, nil, fmt.Errorf("%s: schema '%s', want '%s'", path, r.Schema, core.PlanResultSchema)
	}
	texts := map[string]string{}
	for _, u := range r.Units {
		switch u.Status {
		case core.StatusError, core.StatusNotRun, core.StatusExcluded:
			continue
		}
		text, err := os.ReadFile(filepath.Join(dir, "units", filepath.FromSlash(u.Path), "plan.txt"))
		if err != nil {
			return r, nil, err
		}
		texts[u.Path] = string(text)
	}
	return r, texts, nil
}

// gitTree is the tree id of commit sha in the repository tfpr runs in, or
// "" when it cannot be read.
func gitTree(sha string) string {
	if sha == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", sha+"^{tree}")
	cmd.Env = redact.WithoutTFPR(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fileSHA256 hashes a file without reading it into memory: planfiles can
// be large.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
