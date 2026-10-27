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

// Acceptance (architecture §9, Track A): a large plan's comment fits
// GitHub's limit, puts the truncation banner within its first five lines,
// and lists every delete and replace.
func TestLargePlanComment(t *testing.T) {
	comment := fixtureComment(t, "large")
	if n := utf8.RuneCountInString(comment); n > 65536 {
		t.Errorf("%d characters, over GitHub's 65,536", n)
	}
	head := strings.SplitN(comment, "\n", 6)[:5]
	if !strings.HasPrefix(head[3], "> **Plan text truncated**: showing ") {
		t.Errorf("no banner in the first five lines:\n%s", strings.Join(head, "\n"))
	}
	list := comment[strings.Index(comment, "```diff\n"):]
	list = list[:strings.Index(list, "\n```\n")]
	u := fixtureUnit(t, "large")
	risky := 0
	for _, r := range u.Resources {
		if r.Action == "delete" || r.Action == "replace" {
			risky++
			if !strings.Contains(list, " "+r.Address+" ") {
				t.Errorf("%s %s is not listed", r.Action, r.Address)
			}
		}
	}
	if risky != 30 {
		t.Errorf("the fixture has %d deletes and replaces, want 30", risky)
	}
	if !strings.Contains(comment, "\n+ 2,555 more `create` lines\n") {
		t.Errorf("the create group does not say how many lines it left out")
	}
}

// A stack name lands in <summary> escaped, and in a code span elsewhere.
func TestDetailsEscapesTheSummary(t *testing.T) {
	comment := fixtureComment(t, "html-stack")
	if !strings.Contains(comment, "<summary>Plan output for envs/&lt;prod&gt;&amp;&#34;x&#34;</summary>") {
		t.Errorf("stack name not escaped in <summary>:\n%s", comment)
	}
	if strings.Contains(comment, "<prod>") && !strings.Contains(comment, "`envs/<prod>&\"x\"`") {
		t.Errorf("raw stack name outside a code span:\n%s", comment)
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

// A user template that does not fit even with no plan text gets the
// built-in minimal layout, which always fits and is never cut mid-HTML.
func TestOverflowFallsBackToMinimal(t *testing.T) {
	r := NewPlanResult(PlanResult{Target: Target{Stack: "infra", Workspace: "prod"}}, []Unit{fixtureUnit(t, "large")})
	c := PlanComment{
		Result: r, Texts: map[string]string{".": string(fixtureFile(t, "large", "plan.txt"))}, Host: HostFor("github"), Links: links,
		Template: `{{define "footer"}}{{range .Result.Units}}{{range .Resources}}{{.Address}} {{end}}{{end}}{{end}}`,
	}
	got, err := RenderPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(got); n > 65536 {
		t.Errorf("fallback is %d characters", n)
	}
	for _, want := range []string{"> **Comment shortened**", "> Full plan: [job summary]", "| 3,000 | 40 | 25 | 5 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("fallback lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<details") || strings.Contains(got, "```") {
		t.Errorf("fallback has a plan body:\n%s", got)
	}
}

// When delete, replace and forget lines alone do not fit, the banner says
// so and every group still has a count.
func TestRiskyLinesThatDoNotFit(t *testing.T) {
	u := fixtureUnit(t, "large")
	r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}}, []Unit{u})
	got, err := RenderPlan(PlanComment{Result: r, Host: HostFor("github"), Budget: 1500})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"did not fit here.** Read them in the full plan", "- **25 delete lines not listed**", "+ 3,000 more `create` lines"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if n := utf8.RuneCountInString(got); n > 1500 {
		t.Errorf("%d characters, over 1,500", n)
	}
}

// The summary keeps the unchanged-attribute lines, shows the text expanded,
// counts bytes, and fits its limit.
func TestSummary(t *testing.T) {
	r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}}, []Unit{fixtureUnit(t, "moves-only")})
	c := PlanComment{Result: r, Texts: map[string]string{".": string(fixtureFile(t, "moves-only", "plan.txt"))}, Host: HostFor("github"), Links: links}
	got, err := RenderSummary(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<details open>", "# (1 unchanged attribute hidden)", "[job log](https://ci.example/run/1234/job/5678)"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
	if strings.Contains(got, "#summary") {
		t.Errorf("the summary links to itself")
	}
	comment, _ := RenderPlan(c)
	if strings.Contains(comment, "unchanged attribute hidden") {
		t.Errorf("the comment kept the noise")
	}
	c.Budget = 1200
	got, err = RenderSummary(c)
	if err != nil || len(got) > 1200 || !strings.Contains(got, "bytes") {
		t.Errorf("small summary (%d bytes, %v):\n%s", len(got), err, got)
	}
}

// Several units (Terragrunt) render with a unit table, riskiest unit first,
// and share the plan-text budget.
func TestMultiUnit(t *testing.T) {
	net, app := fixtureUnit(t, "changes"), fixtureUnit(t, "import-update")
	net.Path, app.Path = "net", "app"
	r := NewPlanResult(PlanResult{Target: Target{Stack: "live", Workspace: "prod"}}, []Unit{app, net})
	got, err := RenderPlan(PlanComment{
		Result: r, Host: HostFor("github"), Budget: 4000,
		Texts: map[string]string{"net": string(fixtureFile(t, "changes", "plan.txt")), "app": string(fixtureFile(t, "import-update", "plan.txt"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| Unit | Status |", "| `net` | changes | 1 | 1 | 1 | 1 | 0 | 0 | 0 | 0 |", "Plan output for net", "Plan output for app"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "#### ⚠️ `net`") > strings.Index(got, "#### 📝 `app`") {
		t.Errorf("the destructive unit is not first:\n%s", got)
	}
	if n := utf8.RuneCountInString(got); n > 4000 {
		t.Errorf("%d characters, over 4,000", n)
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

// A failed plan that overflows still says it failed, why (shortened to
// fit) and where to look, never a table of zero counts.
func TestMinimalFailedPlan(t *testing.T) {
	reason := "terraform plan exited 1: " + strings.Repeat("Error: something long. ", 400)
	r := NewPlanResult(PlanResult{Target: Target{Stack: "infra", Workspace: "prod"}}, []Unit{FailedUnit(".", intp(1), reason)})
	got, err := RenderPlan(PlanComment{Result: r, Host: HostFor("github"), Budget: 1500, Links: links})
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(got); n > 1500 {
		t.Errorf("%d characters, over 1,500", n)
	}
	for _, want := range []string{"### ❌ Terraform plan · `infra` (`prod`) · Plan failed", "**Plan failed.**", "Reason: `terraform plan exited 1: Error", "[job log](" + links.Job + ")"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "| Add |") {
		t.Errorf("counts table in a failed plan:\n%s", got)
	}
}

// Once a line does not fit, nothing lower-risk is listed after it, even a
// shorter line that would fit: the list stays in strict risk order.
func TestAllocateKeepsStrictRiskOrder(t *testing.T) {
	var res []Resource
	for i := range 5 {
		res = append(res, Resource{Address: fmt.Sprintf("module.%s.terraform_data.u[%d]", strings.Repeat("long", 20), i), Action: "update"})
		res = append(res, Resource{Address: fmt.Sprintf("c.x[%d]", i), Action: "create"})
	}
	r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}}, []Unit{{Path: ".", Status: StatusChanges, Resources: res}})
	v := newView(PlanComment{Result: r, Host: HostFor("github")}, layout{measure: inRunes})
	update, create := v.Units[0].group("update"), v.Units[0].group("create")
	line := func(g *Group) int { return inRunes.of(g.lines[0]) + 1 }
	if line(create) >= line(update) {
		t.Fatalf("create lines (%d) must be shorter than update lines (%d) for this test", line(create), line(update))
	}
	// Room for two updates and one create line, but not a third update.
	v.allocate(2*line(update)+line(create), 1<<20, inRunes)
	if update.Listed != 2 || create.Listed != 0 {
		t.Errorf("listed %d updates and %d creates, want 2 and 0", update.Listed, create.Listed)
	}
}

// With one unit failed, the result's status is error. The banner must only
// describe what the comment shows; the units that planned are shown.
func TestErrorStatusBannerMatchesTheBody(t *testing.T) {
	net := fixtureUnit(t, "large")
	net.Path = "net"
	r := NewPlanResult(PlanResult{Target: Target{Stack: "live", Workspace: "prod"}}, []Unit{FailedUnit("db", intp(1), "boom"), net})
	got, err := RenderPlan(PlanComment{Result: r, Host: HostFor("github"), Budget: 20000,
		Texts: map[string]string{"net": string(fixtureFile(t, "large", "plan.txt"))}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Plan text truncated") != strings.Contains(got, "Plan output for net (truncated") {
		t.Errorf("the banner and the body disagree about the plan text:\n%s", got)
	}
	if strings.Contains(got, "Resource list shortened") != strings.Contains(got, "more `create` lines") {
		t.Errorf("the banner and the body disagree about the resource list:\n%s", got)
	}
	for _, want := range []string{"**Plan failed.**", "Reason: `boom`", "| `db` | error |", "-   terraform_data.legacy[0]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// The minimal layout is always the built-in one, so a minimal render does
// not parse the user template at all.
func TestMinimalDoesNotParseTheUserTemplate(t *testing.T) {
	r := NewPlanResult(PlanResult{Target: Target{Stack: "s", Workspace: "w"}}, []Unit{fixtureUnit(t, "changes")})
	if _, err := RenderPlan(PlanComment{Result: r, Host: HostFor("github"), Template: "{{define", Minimal: true}); err != nil {
		t.Errorf("parsed the user template: %v", err)
	}
}
