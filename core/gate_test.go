package core

import (
	"strings"
	"testing"
)

// record is a real record line for stack "infra", workspace "default".
func record(status, tree string) string {
	u := Unit{Path: ".", Status: status, Skippable: status == StatusNoOp}
	return Record(NewPlanResult(PlanResult{
		Target: Target{Stack: "infra", Workspace: "default"},
		Source: Source{SHA: "prmerge", Tree: tree},
		Run:    Run{ID: "77", Attempt: 1},
	}, []Unit{u}))
}

func TestGate(t *testing.T) {
	ok := GuardResult{Code: GuardOK}
	noop := record(StatusNoOp, "T")
	base := GateInput{Guard: ok, Key: "infra:default", SkipNoop: true, Attempt: 1, PRs: 1, Records: []string{noop}, MergeTree: "T"}
	with := func(f func(*GateInput)) GateInput { in := base; f(&in); return in }

	for _, c := range []struct {
		name   string
		in     GateInput
		action string
		reason string
	}{
		// The guard comes first, whatever skip-on-no-op would say.
		{"superseded: clean skip", with(func(in *GateInput) { in.Guard.Code = GuardSuperseded }), GateSkip, "superseded"},
		{"superseded, skip-noop off", with(func(in *GateInput) { in.Guard.Code, in.SkipNoop = GuardSuperseded, false }), GateSkip, "superseded"},
		{"unverifiable ref: refuse", with(func(in *GateInput) { in.Guard.Code = GuardUnverified }), GateRefuse, "unverified-ref"},
		// Skip-on-no-op is opt-in.
		{"skip-noop off: apply", with(func(in *GateInput) { in.SkipNoop = false }), GateApply, ""},
		// Every condition must hold to skip; any doubt applies.
		{"all hold: skip", base, GateSkip, "no-op"},
		{"re-run", with(func(in *GateInput) { in.Attempt = 2 }), GateApply, "rerun-or-unknown-attempt"},
		{"no attempt counter", with(func(in *GateInput) { in.Attempt = 0 }), GateApply, "rerun-or-unknown-attempt"},
		{"no PR", with(func(in *GateInput) { in.PRs = 0 }), GateApply, "no-pr"},
		{"two PRs", with(func(in *GateInput) { in.PRs = 2 }), GateApply, "ambiguous-pr"},
		{"no record", with(func(in *GateInput) { in.Records = nil }), GateApply, "no-record"},
		{"two records", with(func(in *GateInput) { in.Records = []string{noop, noop} }), GateApply, "ambiguous-record"},
		{"invalid record", with(func(in *GateInput) { in.Records = []string{"<!-- tfpr:plan@1 {nope} -->"} }), GateApply, "invalid-record"},
		{"record without a record line", with(func(in *GateInput) { in.Records = []string{""} }), GateApply, "invalid-record"},
		{"record for another stack", with(func(in *GateInput) { in.Key = "other:default" }), GateApply, "record-for-another-stack"},
		{"plan had changes", with(func(in *GateInput) { in.Records = []string{record(StatusChanges, "T")} }), GateApply, "plan-has-changes"},
		{"outputs-only plan", with(func(in *GateInput) { in.Records = []string{record(StatusOutputsOnly, "T")} }), GateApply, "plan-has-changes"},
		{"failed plan", with(func(in *GateInput) { in.Records = []string{record(StatusError, "T")} }), GateApply, "plan-has-changes"},
		{"merged tree differs", with(func(in *GateInput) { in.MergeTree = "U" }), GateApply, "tree-differs"},
		{"merged tree unknown", with(func(in *GateInput) { in.MergeTree = "" }), GateApply, "tree-unknown"},
		{"planned tree unknown", with(func(in *GateInput) { in.Records = []string{record(StatusNoOp, "")} }), GateApply, "tree-unknown"},
	} {
		if got := Gate(c.in); got != (GateDecision{c.action, c.reason}) {
			t.Errorf("%s: got %+v, want %s %q", c.name, got, c.action, c.reason)
		}
	}
}

func TestParseRecord(t *testing.T) {
	rec, err := ParseRecord(record(StatusNoOp, "T"))
	if err != nil || rec.Key != "infra:default" || rec.Status != StatusNoOp || rec.Tree != "T" || rec.Run != "77" {
		t.Fatalf("got %+v, %v", rec, err)
	}
	for _, bad := range []string{
		"", "<!-- tf-plan:infra :: default -->", `<!-- tfpr:plan@1 {"key":"k"} --> trailing`,
		`<!-- tfpr:plan@1 {"key":"k","extra":1} -->`, `<!-- tfpr:plan@1 {"key":"k"}{"key":"l"} -->`,
	} {
		if _, err := ParseRecord(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if RecordRun([]string{record(StatusNoOp, "T")}) != "77" || RecordRun(nil) != "" || RecordRun([]string{"x"}) != "" {
		t.Error("RecordRun")
	}
}

func TestGateResultFor(t *testing.T) {
	g := GateResult{Schema: DecisionSchema, SHA: "abc", Stack: "infra", Workspace: "default"}
	if !g.For("abc", "infra", "default") || g.For("def", "infra", "default") || g.For("abc", "other", "default") {
		t.Error("For")
	}
	if strings.Contains(DecisionSchema, " ") {
		t.Error("schema id")
	}
}
