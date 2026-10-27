package core

import (
	"embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*.tmpl"))

// truncationNotice ends a plan body cut to the comment budget.
const truncationNotice = "[truncated — download the tfplan artifact and run `terraform show tfplan` for full output]"

// PlanComment is everything the plan comment shows.
type PlanComment struct {
	Result        PlanResult // exactly one unit in this layout, unless the plan failed
	PlanText      []byte     // that unit's plan text, already filtered; nil when it failed
	RunID, JobURL string     // the run link
	Budget        int        // max bytes of PlanText in the comment
}

// RenderPlan renders the plan comment body. Its second line is the comment
// record (Record).
func RenderPlan(c PlanComment) (string, error) {
	r := c.Result
	if r.Summary.Status == StatusError {
		return renderFailed(c)
	}
	if len(r.Units) != 1 {
		return "", fmt.Errorf("the plan comment layout shows one unit; this result has %d", len(r.Units))
	}
	s := StatsOf(r.Summary.Counts)
	view := struct {
		Stack, Workspace, ExitCode, RunID, JobURL                 string
		Stats                                                     Stats
		Marker, Record, Open, Icon, Counts, DiffSummary, PlanText string
	}{
		Stack: r.Target.Stack, Workspace: r.Target.Workspace, ExitCode: strconv.Itoa(r.Summary.ExitCode),
		RunID: c.RunID, JobURL: c.JobURL, Stats: s,
		Marker: PlanMarker(r.Target.Stack, r.Target.Workspace), Record: Record(r),
	}

	view.Icon = "✅"
	if s.HasOutputChanges() {
		view.Icon = "🔸"
	}
	if s.HasChanges() {
		view.Icon = "📝"
	}
	if s.HasImports() {
		view.Icon = "📥"
	}

	// The wrapper is open when there is something to review.
	switch {
	case s.HasChanges() || s.HasImports():
		view.Counts, view.Open = s.Summary(), " open"
	case s.HasOutputChanges():
		// An output-only plan (exit 2, zero resource changes) is still a real
		// diff: outputs feed terraform_remote_state consumers.
		view.Counts, view.Open = fmt.Sprintf("No resource changes — %d output change(s)", s.OutputChanges), " open"
	default:
		view.Counts = "No changes"
	}

	view.DiffSummary = DiffSummary(c.PlanText)
	view.PlanText = truncate(c.PlanText, c.Budget)
	return execute("plan.md.tmpl", view)
}

// renderFailed renders the comment for a failed plan. It replaces the
// stack's earlier comment, so an older plan never stands as if current.
func renderFailed(c PlanComment) (string, error) {
	r := c.Result
	var reasons []string
	for _, u := range r.Units {
		if u.Error != "" {
			reasons = append(reasons, u.Error)
		}
	}
	return execute("plan-failed.md.tmpl", struct {
		Marker, Record, Stack, Workspace, RunID, JobURL, Reason string
	}{PlanMarker(r.Target.Stack, r.Target.Workspace), Record(r), r.Target.Stack, r.Target.Workspace,
		c.RunID, c.JobURL, strings.Join(reasons, "; ")})
}

// truncate cuts plan text to budget bytes so the comment stays under the
// host's size limit; the full plan is in the tfplan artifact. Trailing
// newlines are dropped either way.
func truncate(text []byte, budget int) string {
	if len(text) <= budget {
		return strings.TrimRight(string(text), "\n")
	}
	return strings.TrimRight(string(text[:budget]), "\n") + "\n\n" + truncationNotice
}

// DiffSummary turns terraform's "  # <address> will be ..." lines into a
// ```diff body: additions +, deletions -, updates and replaces !, reads ~.
// Other "#" lines (such as "# (because ...)") keep a "#" prefix.
func DiffSummary(planText []byte) string {
	var lines []string
	for _, line := range strings.Split(string(planText), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimLeft(line, " \t\n\v\f\r"), "# ")
		if !ok {
			continue
		}
		prefix := "# "
		switch {
		case strings.HasSuffix(rest, "will be created"), strings.HasSuffix(rest, "will be imported"):
			prefix = "+ "
		case strings.HasSuffix(rest, "will be destroyed"):
			prefix = "- "
		case strings.HasSuffix(rest, "will be updated in-place"),
			strings.Contains(rest, "will be replaced"), strings.Contains(rest, "must be replaced"):
			prefix = "! "
		case strings.Contains(rest, "will be read during apply"):
			prefix = "~ "
		}
		lines = append(lines, prefix+rest)
	}
	return strings.Join(lines, "\n")
}

// ApplyComment is everything the apply-result comment shows.
type ApplyComment struct {
	Stack, Workspace string
	Decision         Decision // the plan source that ran, or none
	ExitCode         int
	UsedPlanfile     bool
	Artifact         string // planfile artifact name; "" shows n/a
	PlanRun          string // run that produced the planfile; "" is the current run
	RunID, RunURL    string
	SHA              string
	TerraformVersion string
}

// RenderApply renders the apply-result comment body.
func RenderApply(c ApplyComment) (string, error) {
	view := struct {
		ApplyComment
		Marker, Icon, Status, SourceLine, Reason string
	}{ApplyComment: c, Marker: ApplyMarker(c.Stack, c.Workspace), Reason: c.Decision.Reason}

	view.Icon, view.Status = "✅", "succeeded"
	if c.ExitCode != 0 {
		view.Icon, view.Status = "❌", "failed (exit "+strconv.Itoa(c.ExitCode)+")"
	}

	view.SourceLine = DescribeDecision(c.Decision, 0, "", c.PlanRun).Source
	return execute("apply.md.tmpl", view)
}

func execute(name string, data any) (string, error) {
	var b strings.Builder
	if err := templates.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}
