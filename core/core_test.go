package core

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// cases reads a case table from test/cases: tab-separated, '#' lines are
// headers. The same tables drive the black-box suite.
func cases(t *testing.T, name string, columns int) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "test", "cases", name))
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		row := strings.Split(line, "\t")
		if len(row) != columns {
			t.Fatalf("%s: want %d columns, got %d in %q", name, columns, len(row), line)
		}
		rows = append(rows, row)
	}
	return rows
}

// String is "<source> [reason]", the form the case tables use.
func (d Decision) String() string { return strings.TrimSpace(d.Source + " " + d.Reason) }

func TestDecidePlanSource(t *testing.T) {
	for _, c := range cases(t, "decide_plan_source.tsv", 6) {
		attempt, err := strconv.Atoi(c[2])
		if err != nil {
			t.Fatal(err)
		}
		got := DecidePlanSource(c[1] == "true", attempt, c[3] == "true", c[4] == "true").String()
		if got != c[5] {
			t.Errorf("%s: got %q, want %q", c[0], got, c[5])
		}
	}
}

// fixtureUnit derives the "." unit from a render fixture.
func fixtureUnit(t *testing.T, name string) Unit {
	t.Helper()
	exit, err := strconv.Atoi(strings.TrimSpace(string(fixtureFile(t, name, "exit_code"))))
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := DeriveUnit(UnitInput{Path: ".", PlanJSON: fixtureFile(t, name, "plan.json"), PlanText: string(fixtureFile(t, name, "plan.txt")), ExitCode: &exit})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// fixtureFile reads test/fixtures/render/<name>/<file>, or its gzipped
// copy, <file>.gz (large fixtures).
func fixtureFile(t *testing.T, name, file string) []byte {
	t.Helper()
	path := filepath.Join("..", "test", "fixtures", "render", name, file)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		var f *os.File
		if f, err = os.Open(path + ".gz"); err == nil {
			defer f.Close()
			var z *gzip.Reader
			if z, err = gzip.NewReader(f); err == nil {
				data, err = io.ReadAll(z)
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPlanStats(t *testing.T) {
	for _, c := range cases(t, "plan_stats.tsv", 13) {
		var got []string
		for _, f := range StatsOf(fixtureUnit(t, c[0]).Counts).Fields() {
			got = append(got, f.Value)
		}
		if g, w := strings.Join(got, "|"), strings.Join(c[1:], "|"); g != w {
			t.Errorf("%s:\n got %s\nwant %s", c[0], g, w)
		}
	}
}

func TestArtifactName(t *testing.T) {
	for _, c := range cases(t, "artifact_name.tsv", 4) {
		if got := ArtifactName(c[1], c[2]); got != c[3] {
			t.Errorf("%s: got %q, want %q", c[0], got, c[3])
		}
	}
}

func TestCleanStack(t *testing.T) {
	for _, c := range cases(t, "same_stack.tsv", 4) {
		if got := CleanStack(c[1]) == CleanStack(c[2]); got != (c[3] == "true") {
			t.Errorf("%s: %q and %q: same = %v", c[0], c[1], c[2], got)
		}
	}
	for in, want := range map[string]string{
		"terraform/": "terraform", "./infra": "infra", "stacks//net/": "stacks/net", ".": ".", "./": ".",
	} {
		if got := CleanStack(in); got != want {
			t.Errorf("CleanStack(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDescribeDecision(t *testing.T) {
	for _, c := range []struct {
		d          Decision
		level, log string
		source     string
	}{
		{Decision{SourcePlanfile, ""}, "", "Found planfile", "planfile from PR review (run #77)"},
		{Decision{SourceFresh, "no-artifact-requested"}, "", "", "fresh plan"},
		{Decision{SourceFresh, "rerun"}, "warning", "Re-run detected (attempt=2)", "fresh plan (rerun bypass)"},
		{Decision{SourceFresh, "missing-artifact"}, "warning", "falling back", "fresh plan (artifact missing)"},
		{Decision{SourceNone, "missing-artifact"}, "error", "Planfile artifact 'tfplan-x-default' not found", "none (planfile artifact missing; fresh-plan fallback not allowed)"},
		{Decision{SourceNone, "superseded"}, "", "", "none (superseded tree refused)"},
		{Decision{SourceNone, "unverified-ref"}, "", "", "none (could not verify the branch head)"},
	} {
		got := DescribeDecision(c.d, 2, "tfplan-x-default", "77")
		if got.Level != c.level || got.Source != c.source || !strings.Contains(got.Log, c.log) || (c.log == "") != (got.Log == "") {
			t.Errorf("%+v: got %+v", c.d, got)
		}
	}
}

func TestSupersededGuard(t *testing.T) {
	heads := func(head string, err error) func(string) (string, error) {
		return func(branch string) (string, error) {
			if branch != "main" {
				t.Errorf("looked up %q, want main", branch)
			}
			return head, err
		}
	}
	touched := func(files []string, err error) func(string) ([]string, error) {
		return func(head string) ([]string, error) {
			if head != "def" {
				t.Errorf("compared with %q, want def", head)
			}
			return files, err
		}
	}
	changed := touched([]string{"infra/a.tf"}, nil)
	for _, c := range []struct {
		name    string
		ref     string
		attempt int
		head    func(string) (string, error)
		touched func(string) ([]string, error)
		code    int
		level   string
		msg     string
	}{
		{"branch head", "refs/heads/main", 1, heads("abc", nil), nil, GuardOK, "", "is the head of 'main'"},
		{"branch moved", "refs/heads/main", 1, heads("def", nil), changed, GuardSuperseded, "warning", "commit abc is no longer the head of 'main' (now def) and the stack changed since (infra/a.tf)"},
		{"re-run, branch moved", "refs/heads/main", 2, heads("def", nil), changed, GuardSuperseded, "warning", "(attempt=2)"},
		{"branch moved, many files", "refs/heads/main", 1, heads("def", nil), touched([]string{"a", "b", "c", "d", "e"}, nil), GuardSuperseded, "warning", "(a, b, c, and 2 more)"},
		{"branch moved, stack unchanged", "refs/heads/main", 1, heads("def", nil), touched(nil, nil), GuardOK, "", "abc is no longer the head of 'main' (now def), but nothing in the stack changed since"},
		{"re-run, stack unchanged", "refs/heads/main", 2, heads("def", nil), touched(nil, nil), GuardOK, "", "nothing in the stack changed since"},
		{"branch moved, compare fails", "refs/heads/main", 1, heads("def", nil), touched(nil, errors.New("diverged")), GuardSuperseded, "warning", "tfpr could not tell whether the stack changed since (diverged)"},
		{"branch moved, no compare", "refs/heads/main", 1, heads("def", nil), nil, GuardSuperseded, "warning", "could not tell"},
		{"head lookup fails", "refs/heads/main", 1, heads("", errors.New("boom")), nil, GuardUnverified, "error", "could not read the head of 'main'"},
		{"tag, first attempt", "refs/tags/v1", 1, nil, nil, GuardOK, "warning", "non-branch ref 'refs/tags/v1'"},
		{"tag, re-run", "refs/tags/v1", 2, nil, nil, GuardUnverified, "error", "Re-run apply refused"},
		{"no attempt counter", "", 0, nil, nil, GuardUnverified, "error", "Re-run apply refused"},
	} {
		got := SupersededGuard(c.ref, "abc", c.attempt, c.head, c.touched)
		if got.Code != c.code || got.Level != c.level || !strings.Contains(got.Message, c.msg) {
			t.Errorf("%s: got %+v, want code %d level %q containing %q", c.name, got, c.code, c.level, c.msg)
		}
	}
}

func TestTouching(t *testing.T) {
	files := []string{"infra/main.tf", "/infra2/x.tf", "modules/net/vpc.tf", "README.md"}
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{[]string{"infra"}, "infra/main.tf"},
		{[]string{"infra2"}, "infra2/x.tf"},
		{[]string{"infra", "modules"}, "infra/main.tf modules/net/vpc.tf"},
		{[]string{"modules/net/vpc.tf"}, "modules/net/vpc.tf"},
		{[]string{"other"}, ""},
		{[]string{""}, "infra/main.tf infra2/x.tf modules/net/vpc.tf README.md"},
		{[]string{"."}, "infra/main.tf infra2/x.tf modules/net/vpc.tf README.md"},
	} {
		if got := strings.Join(Touching(files, c.paths), " "); got != c.want {
			t.Errorf("Touching(%q): got %q, want %q", c.paths, got, c.want)
		}
	}
}

func TestParsePaths(t *testing.T) {
	got, err := ParsePaths(" modules/net/ ,\n./shared//vars\n\n.")
	if err != nil || strings.Join(got, " ") != "modules/net shared/vars ." {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"/abs/modules", "../modules", "a/../../b", `infra\modules`, "modules/**", "modules/*", "C:/repo/modules", "mod?"} {
		if _, err := ParsePaths("ok\n" + bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestFailedUnit(t *testing.T) {
	r := NewPlanResult(PlanResult{Target: Target{Stack: "infra", Workspace: "default"}},
		[]Unit{FailedUnit(".", intp(1), "terraform plan exited 1")})
	if r.Summary.Status != StatusError || r.Summary.ExitCode != 1 || r.Summary.Units.Error != 1 || r.Units[0].Skippable {
		t.Errorf("summary %+v", r.Summary)
	}
	body, err := RenderPlan(PlanComment{Result: r, Host: HostFor("github"), Links: Links{Job: "https://ci.example/7"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Record(r), `"status":"error"`) {
		t.Errorf("record: %s", Record(r))
	}
	for _, want := range []string{
		"### ❌ Terraform plan · `infra` (`default`) · Plan failed\n",
		"**Plan failed.** Any earlier plan for `infra` is out of date; see the [job log](https://ci.example/7).",
		"Reason: `terraform plan exited 1`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	body, _ = RenderPlan(PlanComment{Result: r, Host: HostFor("github")})
	if !strings.Contains(body, "see the CI log.") {
		t.Errorf("no run link: %s", body)
	}
}

// Plan text cannot forge the marker or the record: such lines never survive
// in the body.
func TestBodyCannotForgeControlLines(t *testing.T) {
	text := "  <!-- tf-plan:other :: default -->\n<!-- tfpr:plan@1 {\"status\":\"no_op\"} -->\n<!-- tf-apply:x :: y -->\nkeep <!-- tfpr: mid-line\n"
	r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}}, []Unit{{Path: ".", Status: StatusChanges, Counts: Counts{Create: 1}}})
	body, err := RenderPlan(PlanComment{Result: r, Texts: map[string]string{".": text}, Host: HostFor("github")})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			t.Errorf("control-like line in the body: %q", line)
		}
	}
	if !strings.Contains(body, "keep <!-- tfpr: mid-line") {
		t.Errorf("stripped too much:\n%s", body)
	}
}

// The footer leads with the commit reviewers know, the PR head; the merge
// commit that was planned comes second.
func TestFooterShowsPRHead(t *testing.T) {
	for _, c := range []struct{ sha, head, want string }{
		{"7c79a8b1234", "ac9f6121234", "Commit `ac9f612` (planned as merge `7c79a8b`) · "},
		{"7c79a8b1234", "", "Commit `7c79a8b` · "},
		{"7c79a8b1234", "7c79a8b1234", "Commit `7c79a8b` · "},
	} {
		r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}, Source: Source{SHA: c.sha, HeadSHA: c.head}},
			[]Unit{{Path: ".", Status: StatusNoOp, Skippable: true}})
		body, err := RenderPlan(PlanComment{Result: r, Host: HostFor("github"), Texts: map[string]string{".": "No changes.\n"}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("sha %s head %q: want %q in\n%s", c.sha, c.head, c.want, body)
		}
	}
}
