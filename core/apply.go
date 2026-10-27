package core

import (
	"fmt"
	"strconv"
	"strings"
)

// ApplyReport is everything the apply-result comment shows.
type ApplyReport struct {
	Stack, Workspace string
	Host             Host
	Gate             GateResult // the gate's decision: apply, skip or refuse
	ExitCode         int        // terraform apply's exit code, when the gate said apply
	Incomplete       bool       // the gate said apply, but there is no exit code: the apply never finished
	Source           Decision   // tfpr apply: the plan source; zero when the caller applied
	PlanRun          string     // the run that made the planfile, for the source line
	SupersededBy     string     // a link to the run (or commit) that applies instead
	Log              string     // the apply's output, already filtered; "" for none
	SHA, JobURL      string
	ToolVersion      string
	BinaryVersion    string
	Budget           int // characters for the whole body; 0 = the host's limit
}

// Resolved reports whether the result needs nobody's attention: a
// successful apply or a clean skip.
func (r ApplyReport) Resolved() bool {
	switch r.Gate.Action {
	case GateSkip:
		return true
	case GateApply:
		return r.ExitCode == 0 && !r.Incomplete
	}
	return false
}

// ApplyView is what the apply template sees.
type ApplyView struct {
	ApplyReport
	Icon, Status, Explain, SourceLine, LogTitle string
	Failed                                      bool
}

// RenderApplyReport renders the human-readable part of the apply-result
// comment; the adapter adds the marker (ApplyMarker) unfiltered. The apply
// log is cut to fit the budget, keeping its end, where errors and the
// summary line are.
func RenderApplyReport(r ApplyReport) (string, error) {
	if r.Budget <= 0 {
		r.Budget = r.Host.MaxComment
	}
	v := ApplyView{ApplyReport: r}
	switch {
	case r.Gate.Action == GateRefuse:
		v.Icon, v.Status, v.Failed = "🛑", "refused", true
		if strings.HasPrefix(r.Gate.Ref, "refs/heads/") {
			v.Explain = "tfpr could not read the head of " + code(strings.TrimPrefix(r.Gate.Ref, "refs/heads/")) + " to verify that " + code(Short(r.SHA)) + " is still its newest commit, so applying it might roll back later merges. Re-run once the branch head can be read."
		} else {
			v.Explain = "this is a re-run of " + code(r.Gate.Ref) + ", which is not a branch, so tfpr can't tell whether " + code(Short(r.SHA)) + " is still current. Trigger a fresh run instead of re-running this one."
		}
	case r.Gate.Action == GateSkip && r.Gate.Reason == "superseded":
		v.Icon, v.Status = "⏭️", "skipped: superseded"
	case r.Gate.Action == GateSkip:
		v.Icon, v.Status = "⏭️", "skipped: no changes"
	case r.Incomplete:
		v.Icon, v.Status, v.Failed = "⚠️", "did not complete", true
	case r.ExitCode == 0:
		v.Icon, v.Status = "✅", "succeeded"
	default:
		v.Icon, v.Status, v.Failed = "❌", "failed (exit "+strconv.Itoa(r.ExitCode)+")", true
	}
	if r.Gate.Action == GateApply && r.Source.Source != "" {
		v.SourceLine = DescribeDecision(r.Source, 0, "", r.PlanRun).Source
	}

	t := bind(templates, r.Host)
	log := strings.TrimRight(r.Log, "\n")
	v.Log, v.LogTitle = log, "Apply output"
	body, err := execute(t, "apply-report.md.tmpl", v)
	for tries := 0; err == nil && inRunes.of(body) > r.Budget && v.Log != "" && tries < 3; tries++ {
		over := inRunes.of(body) - r.Budget
		v.Log = inRunes.cutTail(v.Log, max(0, inRunes.of(v.Log)-over-200))
		v.LogTitle = fmt.Sprintf("Apply output (last %s of %s characters)", Num(inRunes.of(v.Log)), Num(inRunes.of(log)))
		if strings.TrimSpace(v.Log) == "" {
			v.Log = ""
		}
		body, err = execute(t, "apply-report.md.tmpl", v)
	}
	return StripControlLines(body), err
}
