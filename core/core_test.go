package core

import (
	"errors"
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

func TestPlanStats(t *testing.T) {
	for _, c := range cases(t, "plan_stats.tsv", 13) {
		data, err := os.ReadFile(filepath.Join("..", "test", "fixtures", "render", c[0], "plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		s, err := PlanStats(data)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range s.Fields() {
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

// TestRenderPlanGoldens renders each fixture the way test/run.sh does.
func TestRenderPlanGoldens(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "test", "fixtures", "render", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no render fixtures (%v)", err)
	}
	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			read := func(f string) []byte {
				data, err := os.ReadFile(filepath.Join(dir, f))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			budget := 45000
			if b, err := os.ReadFile(filepath.Join(dir, "budget")); err == nil {
				budget, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
			stats, err := PlanStats(read("plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := RenderPlan(PlanComment{
				Stack: name, Workspace: "default",
				ExitCode: strings.TrimSpace(string(read("exit_code"))),
				RunID:    "1234", JobURL: "https://ci.example/run/1234/job/5678",
				Stats: stats, PlanText: read("plan.txt"), Budget: budget,
			})
			if err != nil {
				t.Fatal(err)
			}
			if want := string(read("expected.md")); got != want {
				t.Errorf("render differs from expected.md:\n%s", got)
			}
		})
	}
}

func TestDiffSummary(t *testing.T) {
	in := "x\n  # a will be created\n\t# b will be read during apply\n# c will be replaced, as requested\n  #no space\n  # d will be imported"
	want := "+ a will be created\n~ b will be read during apply\n! c will be replaced, as requested\n+ d will be imported"
	if got := DiffSummary([]byte(in)); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
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
	for _, c := range []struct {
		name    string
		ref     string
		attempt int
		head    func(string) (string, error)
		code    int
		level   string
		msg     string
	}{
		{"branch head", "refs/heads/main", 1, heads("abc", nil), GuardOK, "", "is the head of 'main'"},
		{"branch moved", "refs/heads/main", 1, heads("def", nil), GuardSuperseded, "error", "commit abc is no longer the head of 'main' (now def)"},
		{"re-run, branch moved", "refs/heads/main", 2, heads("def", nil), GuardSuperseded, "error", "(attempt=2)"},
		{"head lookup fails", "refs/heads/main", 1, heads("", errors.New("boom")), GuardUnverified, "error", "could not read the head of 'main'"},
		{"tag, first attempt", "refs/tags/v1", 1, nil, GuardOK, "warning", "non-branch ref 'refs/tags/v1'"},
		{"tag, re-run", "refs/tags/v1", 2, nil, GuardUnverified, "error", "Re-run apply refused"},
		{"no attempt counter", "", 0, nil, GuardUnverified, "error", "Re-run apply refused"},
	} {
		got := SupersededGuard(c.ref, "abc", c.attempt, c.head)
		if got.Code != c.code || got.Level != c.level || !strings.Contains(got.Message, c.msg) {
			t.Errorf("%s: got %+v, want code %d level %q containing %q", c.name, got, c.code, c.level, c.msg)
		}
	}
}
