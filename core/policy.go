package core

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Plan sources for apply.
const (
	SourcePlanfile = "planfile" // apply the reviewed planfile
	SourceFresh    = "fresh"    // plan again and apply with -auto-approve
	SourceNone     = "none"     // refuse to apply
)

// IsFirstAttempt reports whether attempt is known to be a run's first
// attempt. 0 means the CI has no attempt counter; every policy treats that
// like a re-run, so an unknown attempt never applies a stored planfile and
// always gets the strictest superseded-tree check.
func IsFirstAttempt(attempt int) bool { return attempt == 1 }

// Decision is what apply runs, and why when it is not the reviewed planfile.
type Decision struct{ Source, Reason string }

// DecidePlanSource chooses between the reviewed planfile, a fresh plan and
// refusing to apply.
func DecidePlanSource(usePlanfile bool, attempt int, hasPlanfile, allowFallback bool) Decision {
	switch {
	case !usePlanfile:
		return Decision{SourceFresh, "no-artifact-requested"}
	case !IsFirstAttempt(attempt):
		// The stored plan reflects state before the failed attempt; applying it
		// risks "Saved plan is stale" or a partial roll-forward. The re-run
		// applies a fresh plan instead, which the superseded-tree guard limits to
		// the branch head.
		return Decision{SourceFresh, "rerun"}
	case hasPlanfile:
		return Decision{SourcePlanfile, ""}
	case allowFallback:
		return Decision{SourceFresh, "missing-artifact"}
	default:
		// Fail closed: the reviewed plan is gone, and an unreviewed -auto-approve
		// apply is exactly what the planfile flow exists to prevent.
		return Decision{SourceNone, "missing-artifact"}
	}
}

// DecisionText is how a plan-source decision reads: the line apply logs when
// it decides (at Level: "warning", "error", or "" for plain output; no line
// when Log is empty) and the apply-result comment's Source line.
type DecisionText struct {
	Level, Log, Source string
}

// DescribeDecision is the one table of decision texts. attempt, artifact
// (the planfile artifact name) and planRun (the run that made the planfile,
// "" for the current run) fill in the details.
func DescribeDecision(d Decision, attempt int, artifact, planRun string) DecisionText {
	if planRun == "" {
		planRun = "current"
	}
	switch d {
	case Decision{SourcePlanfile, ""}:
		return DecisionText{"", "Found planfile from artifact; will apply it",
			"planfile from PR review (run #" + planRun + ")"}
	case Decision{SourceFresh, "rerun"}:
		return DecisionText{"warning", fmt.Sprintf("Re-run detected (attempt=%d); skipping stored planfile to avoid stale-plan apply", attempt),
			"fresh plan (rerun bypass)"}
	case Decision{SourceFresh, "missing-artifact"}:
		return DecisionText{"warning", "Planfile artifact requested but not found; falling back to fresh-plan apply (allow_fresh_plan_fallback=true)",
			"fresh plan (artifact missing)"}
	case Decision{SourceNone, "missing-artifact"}:
		return DecisionText{"error", fmt.Sprintf("Planfile artifact '%s' not found; refusing to apply a fresh, unreviewed plan. It may have expired (the plan action's retention_days input sets how long it is kept) or the plan run did not upload it. Re-run the plan, or set allow_fresh_plan_fallback=true to apply a fresh plan instead.", artifact),
			"none (planfile artifact missing; fresh-plan fallback not allowed)"}
	case Decision{SourceNone, "superseded"}: // the guard logs its own reason
		return DecisionText{Source: "none (superseded tree refused)"}
	case Decision{SourceNone, "unverified-ref"}:
		return DecisionText{Source: "none (could not verify the branch head)"}
	}
	return DecisionText{Source: "fresh plan"}
}

// Guard outcomes.
const (
	GuardOK         = 0 // the fresh apply may run
	GuardSuperseded = 1 // the branch head moved on and changed the stack
	GuardUnverified = 2 // the head could not be checked
)

// GuardResult is the superseded-tree guard's verdict plus the message to log
// at Level: "warning", "error", or "" for plain output.
type GuardResult struct {
	Code    int
	Level   string
	Message string
	Head    string // the branch head, when it was read
}

// SupersededGuard decides whether an apply of sha (built from ref) may run.
// If the branch has moved on and the newer commits changed the stack,
// applying sha's tree would roll back what was merged since, so it is a
// clean skip: the run for the newer head applies the stack. If they did not
// change it (touched lists no files), sha's stack is the head's stack and
// the apply runs; under path filters the newer head's run may never apply
// this stack, so skipping would lose sha's change. touched lists the files
// under the stack's paths changed between sha and a head; an error, or a nil
// touched, counts as changed. branchHead looks up the current head of a
// branch.
func SupersededGuard(ref, sha string, attempt int, branchHead func(branch string) (string, error), touched func(head string) ([]string, error)) GuardResult {
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		if !IsFirstAttempt(attempt) {
			return GuardResult{GuardUnverified, "error", fmt.Sprintf(
				"Re-run apply refused: cannot verify staleness for non-branch ref '%s'. Trigger a fresh run instead.", ref), ""}
		}
		// First attempt on a tag or other ref: nothing to compare against.
		return GuardResult{GuardOK, "warning", fmt.Sprintf(
			"Fresh apply of non-branch ref '%s': there is no branch head to compare against, so the superseded-tree check is skipped.", ref), ""}
	}
	head, err := branchHead(branch)
	if err != nil {
		return GuardResult{GuardUnverified, "error", fmt.Sprintf(
			"Fresh apply refused: could not read the head of '%s' to rule out applying a superseded tree.", branch), ""}
	}
	if head == sha {
		return GuardResult{GuardOK, "", fmt.Sprintf("Superseded-tree check passed: %s is the head of '%s'.", sha, branch), head}
	}
	var files []string
	err = errors.New("no way to compare commits")
	if touched != nil {
		files, err = touched(head)
	}
	if err == nil && len(files) == 0 {
		return GuardResult{GuardOK, "", fmt.Sprintf(
			"Superseded-tree check passed: %s is no longer the head of '%s' (now %s), but nothing in the stack changed since, so applying it applies the head's stack.",
			sha, branch, head), head}
	}
	why := "the stack changed since (" + strings.Join(files[:min(len(files), 3)], ", ")
	if len(files) > 3 {
		why += fmt.Sprintf(", and %d more", len(files)-3)
	}
	why += ")"
	if err != nil {
		why = fmt.Sprintf("tfpr could not tell whether the stack changed since (%s)", err)
	}
	return GuardResult{GuardSuperseded, "warning", fmt.Sprintf(
		"Skipping the apply (attempt=%d): commit %s is no longer the head of '%s' (now %s) and %s. Applying its tree would roll back later merges; the run for %s applies these changes.",
		attempt, sha, branch, head, why, head), head}
}

// ParsePaths reads TFPR_PATHS: repo-relative paths separated by newlines
// or commas. An entry that is absolute, leaves the repository, uses
// backslashes or looks like a glob is an error, not a guess: a path that
// matches nothing would let an older tree roll back newer changes.
func ParsePaths(s string) ([]string, error) {
	var paths []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' }) {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			continue
		case strings.HasPrefix(p, "/") || strings.Contains(p, ":") || strings.ContainsAny(p, "\\*?["):
			return nil, fmt.Errorf("TFPR_PATHS entry '%s': want a plain path relative to the repository root, no globs or backslashes", p)
		}
		p = path.Clean(p)
		if p == ".." || strings.HasPrefix(p, "../") {
			return nil, fmt.Errorf("TFPR_PATHS entry '%s' is outside the repository", p)
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// Touching returns the files (repo-relative, slash-separated) at or under
// any of paths (clean, repo-relative). A path of "" or "." is the whole
// repository.
func Touching(files, paths []string) []string {
	var out []string
	for _, f := range files {
		f = strings.TrimPrefix(f, "/")
		for _, p := range paths {
			if p == "" || p == "." || f == p || strings.HasPrefix(f, p+"/") {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
