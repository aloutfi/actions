package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"unicode/utf8"
)

// links are the full-plan links test/run.sh renders with.
var links = Links{
	Job:      "https://ci.example/run/1234/job/5678",
	Summary:  "https://ci.example/run/1234#summary",
	PlanText: "https://ci.example/run/1234/artifacts/plan.txt",
}

// fixtureComment renders a fixture's whole comment the way `tfpr derive`
// then `tfpr render` do in test/run.sh: marker, record, then the body within
// the host limit (or the fixture's budget file).
func fixtureComment(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join("..", "test", "fixtures", "render", name)
	stack := name
	if b, err := os.ReadFile(filepath.Join(dir, "stack")); err == nil {
		stack = strings.TrimSpace(string(b))
	}
	limit := HostFor("github").MaxComment
	if b, err := os.ReadFile(filepath.Join(dir, "budget")); err == nil {
		limit, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	r := NewPlanResult(PlanResult{
		Tool: Tool{Version: "dev", Runner: "byo"}, Target: Target{Stack: stack, Workspace: "default"},
		Redaction: Redaction{Mode: "none"},
	}, []Unit{fixtureUnit(t, name)})
	head := PlanMarker(stack, "default") + "\n" + Record(r) + "\n"
	body, err := RenderPlan(PlanComment{
		Result: r, Texts: map[string]string{".": string(fixtureFile(t, name, "plan.txt"))},
		Host: HostFor("github"), Links: links, Budget: limit - utf8.RuneCountInString(head),
	})
	if err != nil {
		t.Fatal(err)
	}
	return head + body
}

// TestRenderPlanGoldens checks every fixture against the expected.md that
// test/run.sh also checks.
func TestRenderPlanGoldens(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "test", "fixtures", "render", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no render fixtures (%v)", err)
	}
	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			if got, want := fixtureComment(t, name), string(fixtureFile(t, name, "expected.md")); got != want {
				t.Errorf("render differs from expected.md:\n%s", got)
			}
		})
	}
}

func TestWaterfill(t *testing.T) {
	for _, c := range []struct {
		room        int
		sizes, want []int
	}{
		{100, []int{10, 500, 500}, []int{10, 45, 45}},
		{100, []int{10, 20}, []int{10, 20}},
		{-5, []int{10}, []int{0}},
		{90, []int{50, 10, 50}, []int{40, 10, 40}},
	} {
		if got := waterfill(c.room, c.sizes); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("waterfill(%d, %v) = %v, want %v", c.room, c.sizes, got, c.want)
		}
	}
}

// The func map is exactly the six pure functions of architecture §2.3:
// nothing that reads the environment or files.
func TestFuncMapIsSmallAndPure(t *testing.T) {
	var names []string
	for name := range funcMap {
		names = append(names, name)
	}
	sort.Strings(names)
	if got := strings.Join(names, " "); got != "code details fence html plural short" {
		t.Errorf("func map: %s", got)
	}
	for _, tmpl := range []string{`{{env "HOME"}}`, `{{readFile "/etc/passwd"}}`, `{{exec "id"}}`} {
		if _, err := withUser(tmpl); err == nil {
			t.Errorf("%s parsed", tmpl)
		}
	}
}

// Pins Go's text/template behavior that TFPR_TEMPLATE relies on: a later
// Parse redefines the named blocks it defines and keeps the rest, and its
// top-level body replaces the document only when it is not just white space
// and comments.
func TestUserTemplateSemantics(t *testing.T) {
	base := template.Must(template.New("doc").Parse(`[{{template "a" .}}|{{template "b" .}}]{{define "a"}}A{{end}}{{define "b"}}B{{end}}`))
	run := func(user string) string {
		t.Helper()
		c := template.Must(base.Clone())
		template.Must(c.Lookup("doc").Parse(user))
		var b strings.Builder
		if err := c.Execute(&b, nil); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	for user, want := range map[string]string{
		`{{define "b"}}user{{end}}`:                    "[A|user]",
		"\n{{/* note */}}\n{{define \"a\"}}x{{end}}\n": "[x|B]",
		`top {{template "a" .}}`:                       "top A",
	} {
		if got := run(user); got != want {
			t.Errorf("user %q: got %q, want %q", user, got, want)
		}
	}
	var b strings.Builder
	if err := base.Execute(&b, nil); err != nil || b.String() != "[A|B]" {
		t.Errorf("the base set changed: %q %v", b.String(), err)
	}
}

// Overriding only the footer changes only the footer.
func TestUserFooter(t *testing.T) {
	r := NewPlanResult(PlanResult{Target: Target{Stack: "infra", Workspace: "prod"}}, []Unit{fixtureUnit(t, "changes")})
	c := PlanComment{Result: r, Texts: map[string]string{".": string(fixtureFile(t, "changes", "plan.txt"))}, Host: HostFor("github")}
	def, err := RenderPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Template = "{{define \"footer\"}}\nOwners: @infra-team · [Runbook](https://example.invalid/runbook)\n{{end}}\n"
	got, err := RenderPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	cut := strings.LastIndex(def, "\n\n")
	if got != def[:cut]+"\n\nOwners: @infra-team · [Runbook](https://example.invalid/runbook)\n" {
		t.Errorf("footer override changed more than the footer:\n%s", got)
	}
	// The defaults are untouched for the next render.
	if again, _ := RenderPlan(PlanComment{Result: r, Texts: c.Texts, Host: c.Host}); again != def {
		t.Errorf("a user template leaked into the defaults")
	}
	c.Template = "{{define \"footer\"}}{{.Nope}}{{end}}"
	if _, err := RenderPlan(c); err == nil {
		t.Errorf("a broken user template rendered")
	}
}

// A dialect without <details> or diff colours degrades to headings and a
// plain fence.
func TestPlainDialect(t *testing.T) {
	r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}}, []Unit{fixtureUnit(t, "changes")})
	got, err := RenderPlan(PlanComment{Result: r, Texts: map[string]string{".": string(fixtureFile(t, "changes", "plan.txt"))},
		Host: Host{Name: "plain", MaxComment: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<details") || strings.Contains(got, "```diff") || !strings.Contains(got, "#### Plan output for s") || !strings.Contains(got, "```text\n") {
		t.Errorf("plain dialect:\n%s", got)
	}
}
