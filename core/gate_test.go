package core

import (
	"strings"
	"testing"
)

// record is a real record line for stack "infra", workspace "default",
// planned for PR head "H".
func record(status, tree string) string { return recordFor(status, tree, "H") }

func recordFor(status, tree, head string) string {
	u := Unit{Path: ".", Status: status, Skippable: status == StatusNoOp}
	return Record(NewPlanResult(PlanResult{
		Target: Target{Stack: "infra", Workspace: "default"},
		Source: Source{SHA: "prmerge", Tree: tree, HeadSHA: head},
		Run:    Run{ID: "77", Attempt: 1},
	}, []Unit{u}))
}

func TestGate(t *testing.T) {
	ok := GuardResult{Code: GuardOK}
	noop := record(StatusNoOp, "T")
	base := GateInput{Guard: ok, Key: "infra:default", SkipNoop: true, Attempt: 1, PRs: 1, PRHead: "H", Records: []string{noop}, MergeTree: "T"}
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
		{"failed plan", with(func(in *GateInput) { in.Records = []string{record(StatusError, "T")} }), GateApply, "plan-incomplete"},
		{"record for an older push", with(func(in *GateInput) { in.PRHead = "H2" }), GateApply, "record-not-for-pr-head"},
		{"record without a head", with(func(in *GateInput) { in.Records = []string{recordFor(StatusNoOp, "T", "")} }), GateApply, "record-not-for-pr-head"},
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
		`<!-- tfpr:plan@1 {"key":"k"}{"key":"l"} -->`, `<!-- tfpr:plan@2 {"key":"k"} -->`,
		`<!-- tfpr:plan@ {"key":"k"} -->`, `<!-- tfpr:plan@1 not json -->`,
	} {
		if _, err := ParseRecord(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	// A later @1 record may add fields: still readable.
	if rec, err := ParseRecord(`<!-- tfpr:plan@1 {"key":"k","status":"no_op","added_later":{"x":1}} -->`); err != nil || rec.Status != "no_op" {
		t.Errorf("additive field: %+v, %v", rec, err)
	}
	if rec.HeadSHA != "H" {
		t.Errorf("head_sha: %q", rec.HeadSHA)
	}
}

// Only a record planned for the PR's final head, by a plan that completed,
// is trusted: a newer push whose plan was cancelled or is still running
// leaves the older push's record behind, and it must not be used.
func TestTrustedRecord(t *testing.T) {
	for _, c := range []struct {
		name    string
		records []string
		key     string
		head    string
		doubt   string
	}{
		{"planned for the head", []string{record(StatusChanges, "T")}, "infra:default", "H", ""},
		{"older push", []string{record(StatusChanges, "T")}, "infra:default", "push2", "record-not-for-pr-head"},
		{"failed plan", []string{record(StatusError, "")}, "infra:default", "H", "plan-incomplete"},
		{"unit not run", []string{record(StatusNotRun, "")}, "infra:default", "H", "plan-incomplete"},
		{"none", nil, "infra:default", "H", "no-record"},
		{"two", []string{record(StatusNoOp, "T"), record(StatusNoOp, "T")}, "infra:default", "H", "ambiguous-record"},
		{"another stack", []string{record(StatusNoOp, "T")}, "x:default", "H", "record-for-another-stack"},
		{"garbage", []string{"x"}, "infra:default", "H", "invalid-record"},
	} {
		rec, doubt := TrustedRecord(c.records, c.key, c.head)
		if doubt != c.doubt || (doubt == "" && rec.Run != "77") {
			t.Errorf("%s: got %q (run %q), want %q", c.name, doubt, rec.Run, c.doubt)
		}
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
