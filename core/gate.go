package core

// Gate actions.
const (
	GateApply  = "apply"  // run the apply
	GateSkip   = "skip"   // nothing to do here: a clean skip
	GateRefuse = "refuse" // fail: tfpr can't tell whether applying is safe
)

// GateInput is everything the merge-time gate decides from, as plain values.
type GateInput struct {
	Guard     GuardResult // the superseded-tree guard's verdict for this commit
	Key       string      // the target key the records must be for
	SkipNoop  bool        // opt-in: skip applying a plan reviewed as a no-op
	Attempt   int         // 0 = the CI has no attempt counter
	PRs       int         // PRs that merged this commit
	PRHead    string      // that PR's final head commit
	Records   []string    // plan records tfpr's own identity left on that PR for this stack
	MergeTree string      // git tree of the commit being applied; "" if unknown
}

// GateDecision is the gate's verdict and why.
type GateDecision struct{ Action, Reason string }

// Gate decides whether a merge-time apply runs.
//
//   - Superseded (the commit is no longer its branch's head): a clean skip.
//     The run for the newer head applies the newer tree, which includes
//     this commit's changes; applying this one would roll back later merges.
//   - Unverifiable (the head can't be read, or a re-run of a non-branch
//     ref): refuse.
//   - Skip on no-op (opt-in): skip only when every condition holds: a first
//     attempt, exactly one PR, exactly one trusted record for this stack
//     (TrustedRecord), its status no_op, and the merged tree equal to the
//     planned tree. Any doubt applies (fail open to apply, never to skip),
//     and the reason says why.
func Gate(in GateInput) GateDecision {
	switch in.Guard.Code {
	case GuardSuperseded:
		return GateDecision{GateSkip, "superseded"}
	case GuardUnverified:
		return GateDecision{GateRefuse, "unverified-ref"}
	}
	if !in.SkipNoop {
		return GateDecision{GateApply, ""}
	}
	switch {
	case !IsFirstAttempt(in.Attempt):
		return GateDecision{GateApply, "rerun-or-unknown-attempt"}
	case in.PRs == 0:
		return GateDecision{GateApply, "no-pr"}
	case in.PRs > 1:
		return GateDecision{GateApply, "ambiguous-pr"}
	}
	rec, doubt := TrustedRecord(in.Records, in.Key, in.PRHead)
	switch {
	case doubt != "":
		return GateDecision{GateApply, doubt}
	case rec.Status != StatusNoOp:
		return GateDecision{GateApply, "plan-has-changes"}
	case rec.Tree == "" || in.MergeTree == "":
		return GateDecision{GateApply, "tree-unknown"}
	case rec.Tree != in.MergeTree:
		return GateDecision{GateApply, "tree-differs"}
	}
	return GateDecision{GateSkip, "no-op"}
}

// DecisionSchema identifies decision.json.
const DecisionSchema = "tfpr/decision@1"

// GateResult is the gate's reified decision, decision.json in the plan dir.
// apply and report act on it and never decide again.
type GateResult struct {
	Schema     string   `json:"schema"`
	Action     string   `json:"action"` // apply | skip | refuse
	Reason     string   `json:"reason"`
	Stack      string   `json:"stack"`
	Workspace  string   `json:"workspace"`
	SHA        string   `json:"sha"` // the commit decided on
	Ref        string   `json:"ref"`
	BranchHead string   `json:"branch_head,omitempty"` // the branch's head, when it was read
	Attempt    int      `json:"attempt"`
	PRs        []string `json:"prs"`                   // every PR that matched SHA; the result is posted only when there is exactly one
	PR         string   `json:"pr,omitempty"`          // the PR that merged SHA, when exactly one did
	PlanRun    string   `json:"plan_run,omitempty"`    // the run that planned it, from the PR's trusted record
	PRHead     string   `json:"pr_head,omitempty"`     // the PR head that run planned (= the PR's final head)
	PlannedSHA string   `json:"planned_sha,omitempty"` // the commit that run planned (the PR's merge commit)
	Artifact   string   `json:"artifact"`              // the planfile artifact name
}

// For reports whether g decided about this commit, stack and workspace.
func (g GateResult) For(sha, stack, workspace string) bool {
	return g.Schema == DecisionSchema && g.SHA == sha && g.Stack == stack && g.Workspace == workspace
}

// TrustedRecord is the one record (already limited to tfpr's own identity's
// comments) that describes the PR as it merged: for this stack, planned for
// prHead (the PR's final head), and a plan that completed. A record for an
// older push, say one whose newer push's plan was cancelled or is still
// running, describes a plan nobody reviewed for this merge. doubt says why
// there is none: no-record, ambiguous-record, invalid-record,
// record-for-another-stack, record-not-for-pr-head or plan-incomplete.
func TrustedRecord(records []string, key, prHead string) (rec RecordData, doubt string) {
	switch len(records) {
	case 0:
		return rec, "no-record"
	case 1:
	default:
		return rec, "ambiguous-record"
	}
	rec, err := ParseRecord(records[0])
	switch {
	case err != nil:
		return rec, "invalid-record"
	case rec.Key != key:
		return rec, "record-for-another-stack"
	case rec.HeadSHA == "" || rec.HeadSHA != prHead:
		return rec, "record-not-for-pr-head"
	case rec.Status == StatusError || rec.Status == StatusNotRun:
		return rec, "plan-incomplete"
	}
	return rec, ""
}
