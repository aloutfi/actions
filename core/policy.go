package core

import (
	"fmt"
	"strings"
)

// Plan sources for apply.
const (
	SourcePlanfile = "planfile" // apply the reviewed planfile
	SourceFresh    = "fresh"    // plan again and apply with -auto-approve
	SourceNone     = "none"     // refuse to apply
)

// Decision is what apply runs, and why when it is not the reviewed planfile.
type Decision struct{ Source, Reason string }

// String is "<source> [reason]", the form the case tables use.
func (d Decision) String() string { return strings.TrimSpace(d.Source + " " + d.Reason) }

// DecidePlanSource chooses between the reviewed planfile, a fresh plan and
// refusing to apply.
func DecidePlanSource(usePlanfile bool, attempt int, hasPlanfile, allowFallback bool) Decision {
	switch {
	case !usePlanfile:
		return Decision{SourceFresh, "no-artifact-requested"}
	case attempt > 1:
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

// Guard outcomes.
const (
	GuardOK         = 0 // the fresh apply may run
	GuardSuperseded = 1 // the commit is no longer the branch head
	GuardUnverified = 2 // the head could not be checked
)

// GuardResult is the superseded-tree guard's verdict plus the message to log
// at Level: "warning", "error", or "" for plain output.
type GuardResult struct {
	Code    int
	Level   string
	Message string
}

// SupersededGuard decides whether a fresh apply of sha (built from ref) may
// run. A fresh apply runs -auto-approve on this commit's tree; if the branch
// has moved on, applying it rolls back everything merged since. Only the
// branch head may fresh-apply. Planfile applies skip this check, because
// terraform rejects a planfile whose state moved.
//
// attempt 0 means the CI has no attempt counter; it is treated like a re-run.
// branchHead looks up the current head of a branch.
func SupersededGuard(ref, sha string, attempt int, branchHead func(branch string) (string, error)) GuardResult {
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		if attempt != 1 {
			return GuardResult{GuardUnverified, "error", fmt.Sprintf(
				"Re-run apply refused: cannot verify staleness for non-branch ref '%s'. Trigger a fresh run instead.", ref)}
		}
		// First attempt on a tag or other ref: nothing to compare against.
		return GuardResult{GuardOK, "warning", fmt.Sprintf(
			"Fresh apply of non-branch ref '%s': there is no branch head to compare against, so the superseded-tree check is skipped.", ref)}
	}
	head, err := branchHead(branch)
	if err != nil {
		return GuardResult{GuardUnverified, "error", fmt.Sprintf(
			"Fresh apply refused: could not read the head of '%s' to rule out applying a superseded tree.", branch)}
	}
	if head != sha {
		return GuardResult{GuardSuperseded, "error", fmt.Sprintf(
			"Fresh apply refused (attempt=%d): commit %s is no longer the head of '%s' (now %s). Applying a superseded tree with -auto-approve would roll back later merges. Let the newest run for this branch apply, or re-run that one.",
			attempt, sha, branch, head)}
	}
	return GuardResult{GuardOK, "", fmt.Sprintf("Superseded-tree check passed: %s is the head of '%s'.", sha, branch)}
}
