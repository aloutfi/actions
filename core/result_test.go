package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

// planDoc builds a minimal plan JSON document from resource_changes and
// output_changes snippets.
func planDoc(resources, outputs string) []byte {
	return []byte(fmt.Sprintf(`{"format_version":"1.2","resource_changes":[%s],"output_changes":{%s},
		"prior_state":{"values":{"root_module":{"resources":[{"address":"x"}]}}}}`, resources, outputs))
}

func rc(address, actions string, extra string) string {
	return fmt.Sprintf(`{"address":%q,"mode":"managed","change":{"actions":%s%s}}`, address, actions, extra)
}

func TestDeriveUnitActions(t *testing.T) {
	resources := strings.Join([]string{
		rc("a.create", `["create"]`, ``),
		rc("a.update", `["update"]`, ``),
		rc("a.delete", `["delete"]`, ``),
		rc("a.replace1", `["delete","create"]`, ``),
		rc("a.replace2", `["create","delete"]`, ``),
		rc("a.forget", `["forget"]`, ``),
		rc("a.read", `["read"]`, ``),
		rc("a.noop", `["no-op"]`, ``),
		rc("a.import", `["no-op"]`, `,"importing":{"id":"i-1"}`),
		rc("a.importupdate", `["update"]`, `,"importing":{"id":"i-2"}`),
	}, ",")
	// Moves carry previous_address outside change.
	resources += `,{"address":"a.moved","previous_address":"a.old","change":{"actions":["no-op"]}}`
	resources += `,{"address":"a.movedupdate","previous_address":"a.old2","change":{"actions":["update"]}}`
	outputs := `"o1":{"actions":["create"],"after_sensitive":true},"o0":{"actions":["no-op"]},"o2":{"actions":["update"]}`

	u, format, err := DeriveUnit(UnitInput{Path: ".", PlanJSON: planDoc(resources, outputs), PlanText: "text", ExitCode: intp(2)})
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{Create: 1, Update: 3, Delete: 1, Replace: 2, Import: 2, Forget: 1, Move: 2, Read: 1, Output: 2}
	if u.Counts != want || format != "1.2" {
		t.Errorf("counts %+v, format %q\nwant %+v", u.Counts, format, want)
	}
	actions := map[string]string{}
	for _, r := range u.Resources {
		actions[r.Address] = r.Action
	}
	for addr, action := range map[string]string{
		"a.import": "import", "a.importupdate": "update", "a.moved": "move", "a.movedupdate": "update", "a.replace1": "replace",
	} {
		if actions[addr] != action {
			t.Errorf("%s: action %q, want %q", addr, actions[addr], action)
		}
	}
	if _, ok := actions["a.noop"]; ok {
		t.Error("a no-op resource is listed")
	}
	if len(u.Outputs) != 2 || u.Outputs[0] != (Output{Name: "o1", Action: "create", Sensitive: true}) || u.Outputs[1].Name != "o2" {
		t.Errorf("outputs %+v", u.Outputs)
	}
	if u.Status != StatusChanges || u.Skippable || u.StateEmpty || *u.TextChars != 4 || u.Fingerprint != Fingerprint("text") {
		t.Errorf("unit %+v", u)
	}
}

// The derived result never contains values from plan.json.
func TestDeriveUnitCarriesNoValues(t *testing.T) {
	data := []byte(`{"format_version":"1.2","variables":{"pw":{"value":"hunter2"}},
		"resource_changes":[{"address":"a.b","change":{"actions":["update"],"before":{"pw":"hunter2"},"after":{"pw":"hunter3"}}}],
		"output_changes":{"o":{"actions":["update"],"before":"hunter2","after":"hunter3"}},
		"prior_state":{"values":{"root_module":{"resources":[{"values":{"pw":"hunter2"}}]}}}}`)
	u, _, err := DeriveUnit(UnitInput{Path: ".", PlanJSON: data})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(NewPlanResult(PlanResult{}, []Unit{u}))
	if strings.Contains(string(out), "hunter") {
		t.Errorf("a value leaked into the result: %s", out)
	}
}

func TestUnitStatus(t *testing.T) {
	for _, c := range []struct {
		counts Counts
		exit   *int
		want   string
	}{
		{Counts{}, intp(0), StatusNoOp},
		{Counts{}, nil, StatusNoOp},
		{Counts{Create: 1}, intp(1), StatusError},
		{Counts{Delete: 1, Move: 1}, intp(2), StatusChanges},
		{Counts{Move: 1}, intp(0), StatusStateOnly},
		{Counts{Import: 1}, nil, StatusStateOnly},
		{Counts{Output: 1}, intp(2), StatusOutputsOnly},
		{Counts{Read: 1}, intp(0), StatusNoOp},
		{Counts{}, intp(2), StatusChanges}, // terraform saw a diff: never no_op
	} {
		if got := unitStatus(c.counts, c.exit); got != c.want {
			t.Errorf("%+v exit %v: got %s, want %s", c.counts, c.exit, got, c.want)
		}
	}
}

func TestParsePlanJSONIsStrict(t *testing.T) {
	good := string(planDoc("", ""))
	for name, data := range map[string]string{
		"trailing junk":  good + "\nWarning: x",
		"leading junk":   "Warning: x\n" + good,
		"two documents":  good + good,
		"truncated":      good[:len(good)/2],
		"no version":     `{"resource_changes":[]}`,
		"not an object":  `[]`,
		"empty":          ``,
		"scrubbed value": strings.Replace(good, `"1.2"`, `***`, 1),
	} {
		if _, _, err := DeriveUnit(UnitInput{Path: ".", PlanJSON: []byte(data)}); !errors.Is(err, ErrPlanJSON) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, _, err := DeriveUnit(UnitInput{Path: ".", PlanJSON: []byte(" \n" + good + "\n\n")}); err != nil {
		t.Errorf("surrounding whitespace: %v", err)
	}
}

func TestSummarize(t *testing.T) {
	units := []Unit{
		{Path: "a", Status: StatusNoOp, Skippable: true},
		{Path: "b", Status: StatusChanges, Counts: Counts{Create: 2}},
		{Path: "c", Status: StatusOutputsOnly, Counts: Counts{Output: 1}, Provisional: true},
		{Path: "d", Status: StatusExcluded},
	}
	s := Summarize(units)
	want := Summary{ExitCode: 2, Status: StatusChanges, Counts: Counts{Create: 2, Output: 1},
		Units: UnitCounts{Total: 4, Changed: 2, Provisional: 1, ApplyNeeded: 2}}
	if s != want {
		t.Errorf("got %+v\nwant %+v", s, want)
	}
	units = append(units, Unit{Path: "e", Status: StatusNotRun})
	if s := Summarize(units); s.ExitCode != 1 || s.Status != StatusNotRun || s.Units.NotRun != 1 {
		t.Errorf("not_run: %+v", s)
	}
	if s := Summarize([]Unit{{Path: ".", Status: StatusNoOp, Skippable: true}}); s.ExitCode != 0 || s.Status != StatusNoOp {
		t.Errorf("no-op: %+v", s)
	}
}

func TestNewPlanResultAndRecord(t *testing.T) {
	r := NewPlanResult(PlanResult{
		Target: Target{Stack: "stacks/net", Workspace: "prod"},
		Source: Source{SHA: "abc", Tree: "def"},
		Run:    Run{ID: "9", Attempt: 1},
	}, []Unit{
		{Path: "z", Status: StatusNoOp, Skippable: true, Fingerprint: "sha256:2", Planfile: Planfile{Present: true, SHA256: "bb"}},
		{Path: "a", Status: StatusChanges, Provisional: true, Fingerprint: "sha256:1", Planfile: Planfile{Present: true, SHA256: "aa"}},
	})
	if r.Schema != PlanResultSchema || r.Target.Key != "stacks-net:prod" || r.Units[0].Path != "a" {
		t.Errorf("result %+v", r)
	}
	if r.Planset == nil || r.Planset.SHA256 != sha256Hex("a aa\nz bb\n") {
		t.Errorf("planset %+v", r.Planset)
	}
	rec := Record(r)
	want := `<!-- tfpr:plan@1 {"key":"stacks-net:prod","sha":"abc","tree":"def","exit":2,"status":"changes","units":2,` +
		`"apply":["a"],"provisional":["a"],"planset":"sha256:` + r.Planset.SHA256 + `","fp":"` +
		Fingerprint("a sha256:1\nz sha256:2\n") + `","run":"9","attempt":1} -->`
	if rec != want {
		t.Errorf("record\n got %s\nwant %s", rec, want)
	}
}

func TestRecordIsSafeInsideAnHTMLComment(t *testing.T) {
	r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w-->x"}}, []Unit{{Path: "a-->b", Status: StatusChanges}})
	body := strings.TrimSuffix(strings.TrimPrefix(Record(r), "<!--"), "-->")
	if strings.Contains(body, "-->") {
		t.Errorf("record can close the comment early: %s", Record(r))
	}
}

func TestRecordOverflowAppliesAll(t *testing.T) {
	var units []Unit
	for i := 0; i < 500; i++ {
		units = append(units, Unit{Path: fmt.Sprintf("units/some/long/path/%04d", i), Status: StatusChanges})
	}
	rec := Record(NewPlanResult(PlanResult{}, units))
	if len(rec) > recordLimit || !strings.Contains(rec, `"apply":"all"`) {
		t.Errorf("len %d: %.200s", len(rec), rec)
	}
}

// The record is not filtered, so it carries only identifiers and hashes:
// never addresses, output names or plan text.
func TestRecordCarriesNothingSensitive(t *testing.T) {
	u, _, err := DeriveUnit(UnitInput{Path: ".", PlanText: "secret-in-text",
		PlanJSON: planDoc(rc(`a.b["secret-key"]`, `["create"]`, ``), `"secret_output":{"actions":["create"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	rec := Record(NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}}, []Unit{u}))
	if strings.Contains(rec, "secret") {
		t.Errorf("record leaks: %s", rec)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(rec, "<!-- tfpr:plan@1 "), " -->")), &fields); err != nil {
		t.Fatal(err)
	}
	allowed := "key sha tree exit status units apply provisional planset fp run attempt"
	for k := range fields {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			t.Errorf("record field %q is not on the allow list", k)
		}
	}
	if len(fields) != len(strings.Fields(allowed)) {
		t.Errorf("record fields %v", fields)
	}
}

// Terraform 1.16 plans a replacement under lifecycle { destroy = false } as
// ["forget","create"] ("must be replaced, but the existing object will not be
// destroyed"). It counts as a create and a forget, like the bash core did.
func TestForgetWithCreate(t *testing.T) {
	resources := rc("a.x", `["forget","create"]`, ``) + "," +
		rc("a.y", `["create","forget"]`, ``) + "," + rc("a.z", `["forget"]`, ``)
	u, _, err := DeriveUnit(UnitInput{Path: ".", PlanJSON: planDoc(resources, "")})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Counts{Create: 2, Forget: 3}); u.Counts != want {
		t.Errorf("counts %+v, want %+v", u.Counts, want)
	}
	if r := u.Resources[0]; r.Action != "create" || !r.Forget {
		t.Errorf("resource %+v", r)
	}
	if u.Resources[2].Forget {
		t.Error("a plain forget is not flagged as forget-with-create")
	}
	if s := StatsOf(u.Counts); s.Creates != 2 || s.Forgets != 3 || !s.HasChanges() {
		t.Errorf("stats %+v", s)
	}
}
