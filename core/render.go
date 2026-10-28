package core

import (
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// templates holds the default templates. Renders work on clones, so a user
// template never changes them and the fallback is always the built-in one.
var templates = template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.tmpl"))

// planTemplate is the document TFPR_TEMPLATE overrides.
const planTemplate = "plan.md.tmpl"

// Layout constants (architecture §6.3, §10.7).
const (
	listShare    = 30   // percent of the comment budget the resource list may take
	summaryShare = 50   // the same, in the job summary
	minBody      = 1000 // a unit whose plan-text share is smaller shows none
	maxOutputs   = 50   // output names listed before "and N more"
)

// riskOrder is the order the resource list follows, riskiest first.
// Groups marked risky are never shortened.
var riskOrder = []struct {
	action, sign string
	risky        bool
}{
	{"delete", "-  ", true},
	{"replace", "-/+", true},
	{"forget", "~  ", true},
	{"update", "!  ", false},
	{"import", ">  ", false},
	{"move", ">  ", false},
	{"create", "+  ", false},
	{"read", "<= ", false},
}

// PlanComment is what the plan comment (and the job summary) is rendered from.
type PlanComment struct {
	Result PlanResult
	// Texts holds each unit's plan text, already filtered, by unit path. A
	// unit with no entry (a failed plan) shows no text.
	Texts map[string]string
	Host  Host
	// Budget is the most the rendered body may measure; 0 means the host's
	// limit. For a comment the caller subtracts the marker and record lines.
	Budget int
	Links  Links
	// Template is the text of a user template (TFPR_TEMPLATE): {{define}}
	// blocks that replace the defaults' blocks of the same name, plus, if it
	// has any, a top-level body that replaces the whole document.
	Template string
	// KeepUnchanged keeps terraform's "# (N unchanged ... hidden)" lines in
	// the comment. The job summary always keeps them.
	KeepUnchanged bool
	// Minimal forces the fallback layout: headline, banner, counts (or why the
	// plan failed) and links.
	Minimal bool
}

// Links are where the full plan can be read. Any may be empty.
type Links struct {
	Job      string // the job log
	Summary  string // the job summary, with the full plan
	PlanText string // the plan.txt artifact
}

// Any reports whether there is a link to the full plan.
func (l Links) Any() bool { return l.Job != "" || l.Summary != "" || l.PlanText != "" }

// PlanView is the data every plan template block receives.
type PlanView struct {
	Result   PlanResult
	Host     Host
	Links    Links
	Status   string // Result.Summary.Status
	Icon     string
	Headline string // "3 to add, 1 to change, 2 to destroy", "No changes", ...
	Counts   Tally  // summed over units
	Drift    Num    // resources changed outside terraform, summed over units
	Multi    bool   // more than one unit: show the unit table and unit headings
	Units    []*UnitView
	Outputs  []Output // changed outputs, at most maxOutputs
	// MoreOutputs counts the changed outputs not in Outputs.
	MoreOutputs Num
	Reason      string // why the plan failed (status error)
	Banner      Banner
}

// Banner is what the lines under the status line report. Show is false
// when none applies.
type Banner struct {
	Show bool
	// Truncated means some plan text is cut; Shown and Total measure it, in
	// Measure ("characters" or "bytes"), over the TruncatedUnits units.
	Truncated      bool
	TruncatedUnits Num
	Shown, Total   Num
	Measure        string
	// ListComplete means every resource line is listed. Otherwise Unlisted
	// lower-risk lines are counted only, and RiskUnlisted delete, replace
	// and forget lines did not fit at all.
	ListComplete bool
	Unlisted     Num
	RiskUnlisted Num
	Forget       Num  // resources to be forgotten: removed from state, not destroyed
	Fallback     bool // the minimal layout replaced the full one
}

// UnitView is one unit's part of the comment. It embeds the Unit (Path,
// Status, Counts, Outputs ...).
type UnitView struct {
	Unit
	Host     Host
	Multi    bool
	Open     bool   // show the plan text expanded
	Label    string // the stack (one unit) or the unit path
	Icon     string
	Headline string
	ListLang string  // fence language for the resource list: diff or text
	Measure  string  // how Shown and Total count: characters or bytes
	Groups   []Group // risk-ordered resource groups with any lines
	// ResourceLines is the number of resource lines; List holds the listed
	// ones, one per line.
	ResourceLines Num
	List          string
	// Body is the plan text shown: filtered, without unchanged-attribute
	// noise (comment only), and cut to fit. Shown and Total measure it.
	Body         string
	Shown, Total Num
	Truncated    bool
	Omitted      bool // the plan text did not fit at all
	full         string
}

// Group is one action's resource lines.
type Group struct {
	Action string
	Risky  bool // delete, replace or forget: listed in full or not at all
	Total  Num
	Listed Num
	More   Num // Total - Listed
	lines  []string
}

// Tally is Counts with printable numbers.
type Tally struct {
	Create, Update, Delete, Replace, Import, Forget, Move, Read, Output Num
}

func tallyOf(c Counts) Tally {
	return Tally{Num(c.Create), Num(c.Update), Num(c.Delete), Num(c.Replace), Num(c.Import), Num(c.Forget), Num(c.Move), Num(c.Read), Num(c.Output)}
}

// layout is how one rendering is budgeted.
type layout struct {
	budget    int
	measure   measure
	listShare int
	keepNoise bool
	open      bool
}

// errOverflow means the rendering does not fit its budget.
var errOverflow = errors.New("does not fit")

// RenderPlan renders the human-readable part of the plan comment: the part
// that goes through the redaction filter. The comment's first two lines, the
// marker (PlanMarker) and the record (Record), are tfpr's own and are
// prepended unfiltered by the repo-host adapter; the body can never contain
// such lines (StripControlLines).
//
// The body fits c.Budget characters. The resource list takes at most 30% of
// it, except that delete, replace and forget lines are always listed in full
// while they fit; the plan text gets the rest. If a user template still
// overflows, the built-in minimal layout is rendered instead.
func RenderPlan(c PlanComment) (string, error) {
	l := layout{budget: c.Budget, measure: inRunes, listShare: listShare, keepNoise: c.KeepUnchanged}
	if l.budget <= 0 {
		l.budget = c.Host.MaxComment
	}
	t := templates
	if c.Template != "" && !c.Minimal { // the minimal layout is always the built-in one
		var err error
		if t, err = withUser(c.Template); err != nil {
			return "", err
		}
	}
	return render(bind(t, c.Host), c, l)
}

// RenderSummary renders the job summary: the same layout with the full
// filtered plan text (unchanged-attribute lines kept), expanded, within the
// host's summary limit in bytes.
func RenderSummary(c PlanComment) (string, error) {
	l := layout{budget: c.Budget, measure: inBytes, listShare: summaryShare, keepNoise: true, open: true}
	if l.budget <= 0 {
		l.budget = c.Host.MaxSummary
	}
	c.Links.Summary = "" // no link to itself
	return render(bind(templates, c.Host), c, l)
}

func render(t *template.Template, c PlanComment, l layout) (string, error) {
	v := newView(c, l)
	if !c.Minimal {
		out, err := fit(t, v, l)
		if !errors.Is(err, errOverflow) {
			return StripControlLines(out), err
		}
	}
	// The fallback: never a comment cut off mid-HTML.
	v.Banner = Banner{Show: true, Fallback: true, ListComplete: true, Forget: v.Banner.Forget, Measure: v.Banner.Measure}
	t = bind(templates, c.Host)
	out, err := execute(t, "minimal", v)
	if err == nil && v.Reason != "" && l.measure.of(out) > l.budget {
		out, err = shortenReason(t, v, l)
	}
	if err == nil && l.measure.of(out) > l.budget {
		err = fmt.Errorf("the plan comment does not fit in %d %s, even in the minimal layout", l.budget, l.measure)
	}
	return StripControlLines(out), err
}

// shortenReason renders the minimal layout of a failed plan with its
// reason cut to fit, ending in an ellipsis.
func shortenReason(t *template.Template, v *PlanView, l layout) (string, error) {
	full := v.Reason
	v.Reason = ""
	out, err := execute(t, "minimal", v)
	if err != nil {
		return out, err
	}
	// The reason's code span and ellipsis take a few characters more.
	room := l.budget - l.measure.of(out) - 8
	for room > 0 {
		v.Reason = l.measure.cut(full, room) + "…"
		if out, err = execute(t, "minimal", v); err != nil || l.measure.of(out) <= l.budget {
			return out, err
		}
		room -= l.measure.of(out) - l.budget
	}
	v.Reason = ""
	return execute(t, "minimal", v)
}

// withUser is the default templates with a user template parsed over them.
// text/template keeps a block the user does not define, replaces one they
// do, and keeps the default document unless the user's top level has
// content (TestUserTemplateSemantics pins this).
func withUser(text string) (*template.Template, error) {
	t, err := templates.Clone()
	if err != nil {
		return nil, err
	}
	if _, err := t.Lookup(planTemplate).Parse(text); err != nil {
		return nil, fmt.Errorf("TFPR_TEMPLATE: %w", err)
	}
	return t, nil
}

// bind is a clone of t whose details func follows host's dialect.
func bind(t *template.Template, host Host) *template.Template {
	c := template.Must(t.Clone())
	return c.Funcs(funcsFor(host))
}

func newView(c PlanComment, l layout) *PlanView {
	r := c.Result
	v := &PlanView{
		Result: r, Host: c.Host, Links: c.Links, Status: r.Summary.Status,
		Counts: tallyOf(r.Summary.Counts), Multi: len(r.Units) > 1,
	}
	v.Icon, v.Headline = headline(r.Summary.Status, r.Summary.Counts)
	v.Banner = Banner{Forget: Num(r.Summary.Counts.Forget), Measure: l.measure.String()}
	listLang := "text"
	if c.Host.DiffFence {
		listLang = "diff"
	}
	var reasons []string
	for _, u := range r.Units {
		if u.Status == StatusExcluded {
			continue
		}
		v.Drift += Num(u.Drift)
		for _, o := range u.Outputs {
			if len(v.Outputs) < maxOutputs {
				v.Outputs = append(v.Outputs, o)
			} else {
				v.MoreOutputs++
			}
		}
		if u.Error != "" {
			reasons = append(reasons, u.Error)
		}
		uv := &UnitView{Unit: u, Host: c.Host, Multi: v.Multi, Open: l.open, Label: r.Target.Stack, ListLang: listLang, Measure: l.measure.String()}
		if v.Multi {
			uv.Label = u.Path
		}
		uv.Icon, uv.Headline = headline(u.Status, u.Counts)
		if text, ok := c.Texts[u.Path]; ok && !(v.Multi && u.Status == StatusNoOp) {
			text = strings.TrimRight(text, "\n")
			if !l.keepNoise {
				text = dropUnchanged(text)
			}
			uv.full, uv.Total = text, Num(l.measure.of(text))
		}
		uv.Groups = groups(u.Resources)
		for _, g := range uv.Groups {
			uv.ResourceLines += g.Total
		}
		v.Units = append(v.Units, uv)
	}
	v.Reason = strings.Join(reasons, "; ")
	// Riskiest unit first; plain terraform has one.
	sort.SliceStable(v.Units, func(i, j int) bool { return unitRank(v.Units[i]) > unitRank(v.Units[j]) })
	return v
}

// fit renders v within l.budget (architecture §2.3 and §6.3):
//
//  1. Render with no list and no plan text: the fixed overhead.
//  2. List delete/replace/forget groups whole while they fit the room left,
//     then lower-risk lines in risk order up to listShare of the budget.
//  3. Render again with the list, to measure everything but the plan text.
//     If the full texts do not fit the rest, cut them at line boundaries,
//     sharing the room between units by water-filling.
//
// A margin is kept for the filter, which runs after rendering and may
// lengthen the text. It returns errOverflow if the result still does not fit.
func fit(t *template.Template, v *PlanView, l layout) (string, error) {
	m, margin := l.measure, l.budget/32
	measure := func() (int, error) {
		out, err := execute(t, planTemplate, v)
		return m.of(out), err
	}

	for _, u := range v.Units {
		u.Body = ""
		for i := range u.Groups {
			u.Groups[i].Listed = 0
		}
	}
	v.syncList()
	base, err := measure()
	if err != nil {
		return "", err
	}
	v.allocate(l.budget*l.listShare/100, l.budget-base-margin, m)

	// Each text as one character, so the block around it is measured too.
	withText, need := 0, 0
	for _, u := range v.Units {
		if u.full != "" {
			u.Body = "x"
			withText++
			need += int(u.Total)
		}
	}
	overhead, err := measure()
	if err != nil {
		return "", err
	}
	overhead -= withText
	if overhead+need+margin > l.budget {
		// The banner and summaries now say "truncated": measure again with
		// the longest numbers they can show.
		for _, u := range v.Units {
			u.Truncated, u.Shown = u.full != "", u.Total
		}
		v.syncText()
		if overhead, err = measure(); err != nil {
			return "", err
		}
		overhead -= withText
	}
	room := l.budget - overhead - margin
	var sizes []int
	for _, u := range v.Units {
		sizes = append(sizes, int(u.Total))
	}
	for i, share := range waterfill(room, sizes) {
		u := v.Units[i]
		switch {
		case u.full == "":
			u.Body, u.Shown, u.Truncated, u.Omitted = "", 0, false, false
		case share >= int(u.Total):
			u.Body, u.Shown, u.Truncated, u.Omitted = u.full, u.Total, false, false
		case share < minBody:
			u.Body, u.Shown, u.Truncated, u.Omitted = "", 0, true, true
		default:
			u.Body = m.cut(u.full, share)
			u.Shown, u.Truncated, u.Omitted = Num(m.of(u.Body)), true, false
		}
	}
	v.syncText()
	out, err := execute(t, planTemplate, v)
	if err == nil && m.of(out) > l.budget {
		err = errOverflow
	}
	return out, err
}

// allocate chooses the listed resource lines. Delete, replace and forget
// groups are listed whole, in risk order, while they fit room. Other lines
// follow in strict risk order until the first one that would take the whole
// list past share (or room).
func (v *PlanView) allocate(share, room int, m measure) {
	used := 0
	cost := func(line string) int { return m.of(line) + 1 }
	for _, rk := range riskOrder {
		for _, u := range v.Units {
			g := u.group(rk.action)
			if g == nil || !g.Risky {
				continue
			}
			n := 0
			for _, line := range g.lines {
				n += cost(line)
			}
			if used+n <= room {
				g.Listed, used = g.Total, used+n
			}
		}
	}
	// The first line that does not fit ends the list, so a shorter
	// lower-risk line never follows a higher-risk line left out.
	limit := min(share, room)
lower:
	for _, rk := range riskOrder {
		for _, u := range v.Units {
			g := u.group(rk.action)
			if g == nil || g.Risky {
				continue
			}
			for _, line := range g.lines {
				if used+cost(line) > limit {
					break lower
				}
				used += cost(line)
				g.Listed++
			}
		}
	}
	v.syncList()
}

// syncList derives each unit's List and the banner's list fields from the
// groups' Listed counts.
func (v *PlanView) syncList() {
	b := &v.Banner
	b.Unlisted, b.RiskUnlisted = 0, 0
	for _, u := range v.Units {
		var lines []string
		for i := range u.Groups {
			g := &u.Groups[i]
			g.More = g.Total - g.Listed
			lines = append(lines, g.lines[:g.Listed]...)
			if g.Risky {
				b.RiskUnlisted += g.More
			} else {
				b.Unlisted += g.More
			}
		}
		u.List = strings.Join(lines, "\n")
	}
	b.ListComplete = b.Unlisted+b.RiskUnlisted == 0
	v.syncBanner()
}

// syncText derives the banner's text fields from the units'.
func (v *PlanView) syncText() {
	b := &v.Banner
	b.Truncated, b.TruncatedUnits, b.Shown, b.Total = false, 0, 0, 0
	for _, u := range v.Units {
		if u.Truncated {
			b.Truncated = true
			b.TruncatedUnits++
			b.Shown += u.Shown
			b.Total += u.Total
		}
	}
	v.syncBanner()
}

func (v *PlanView) syncBanner() {
	b := &v.Banner
	b.Show = b.Fallback || b.Truncated || !b.ListComplete || b.Forget > 0
}

func (u *UnitView) group(action string) *Group {
	for i := range u.Groups {
		if u.Groups[i].Action == action {
			return &u.Groups[i]
		}
	}
	return nil
}

// groups sorts resources into risk-ordered groups of display lines. A
// replacement whose old object is forgotten (lifecycle destroy = false) is a
// forget: that is its risk.
func groups(resources []Resource) []Group {
	width := 0
	for _, r := range resources {
		width = max(width, inRunes.of(r.Address))
	}
	width = min(width, 60)
	byAction := map[string][]string{}
	for _, r := range resources {
		action := r.Action
		if r.Forget {
			action = "forget"
		}
		byAction[action] = append(byAction[action], resourceLine(r, width))
	}
	var out []Group
	for _, rk := range riskOrder {
		lines := byAction[rk.action]
		if len(lines) == 0 {
			continue
		}
		for i := range lines {
			lines[i] = rk.sign + " " + lines[i]
		}
		out = append(out, Group{Action: rk.action, Risky: rk.risky, Total: Num(len(lines)), lines: lines})
	}
	return out
}

// resourceLine is "<address>  <what happens>", the address padded to width.
func resourceLine(r Resource, width int) string {
	desc := r.Action
	if r.Forget {
		desc = "replace, old object forgotten"
	}
	if r.Importing && r.Action != "import" {
		desc += ", import"
	}
	if r.Moved {
		if r.Action == "move" {
			desc = "move from " + r.PreviousAddress
		} else {
			desc += ", moved from " + r.PreviousAddress
		}
	}
	pad := max(0, width-inRunes.of(r.Address))
	return r.Address + strings.Repeat(" ", pad) + "  " + desc
}

// waterfill shares room between sizes: the smallest first, each getting at
// most an equal share of what is left, so unused share flows to the rest.
func waterfill(room int, sizes []int) []int {
	order := make([]int, len(sizes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return sizes[order[a]] < sizes[order[b]] })
	shares := make([]int, len(sizes))
	left := max(room, 0)
	for k, i := range order {
		s := min(sizes[i], left/(len(order)-k))
		shares[i], left = s, left-s
	}
	return shares
}

// headline is a status's icon and phrase (architecture §6.2).
func headline(status string, c Counts) (icon, phrase string) {
	switch status {
	case StatusError:
		return "❌", "Plan failed"
	case StatusNotRun:
		return "⏭️", "Not run"
	case StatusExcluded:
		return "➖", "Excluded"
	case StatusNoOp:
		return "✅", "No changes"
	case StatusOutputsOnly:
		verb := "changes"
		if c.Output != 1 {
			verb = "change"
		}
		return "🔸", fmt.Sprintf("No resource changes; %s %s. Downstream `terraform_remote_state` readers are affected",
			plural(c.Output, "output"), verb)
	case StatusStateOnly:
		var parts []string
		for _, p := range []struct {
			n    int
			verb string
		}{{c.Move, "moved"}, {c.Import, "imported"}, {c.Forget, "forgotten"}} {
			if p.n > 0 {
				parts = append(parts, Num(p.n).String()+" "+p.verb)
			}
		}
		return "🔀", "No infrastructure changes; state-only: " + strings.Join(parts, ", ")
	}
	phrase = fmt.Sprintf("%s to add, %s to change, %s to destroy", Num(c.Create), Num(c.Update), Num(c.Delete))
	for _, p := range []struct {
		n    int
		verb string
	}{{c.Replace, "replace"}, {c.Import, "import"}, {c.Move, "move"}, {c.Forget, "forget"}} {
		if p.n > 0 {
			phrase += fmt.Sprintf(", %s to %s", Num(p.n), p.verb)
		}
	}
	icon = "📝"
	if c.Delete+c.Replace+c.Forget > 0 {
		icon = "⚠️"
	}
	return icon, phrase
}

// unitRank orders units riskiest first (architecture §10.7).
func unitRank(u *UnitView) int {
	switch {
	case u.Status == StatusError:
		return 6
	case u.Status == StatusNotRun:
		return 5
	case u.Counts.Delete+u.Counts.Replace+u.Counts.Forget > 0:
		return 4
	case u.Status == StatusChanges:
		return 3
	case u.Status == StatusStateOnly:
		return 2
	case u.Status == StatusOutputsOnly:
		return 1
	}
	return 0
}

// controlPrefixes start the lines tfpr reserves for itself in a comment.
var controlPrefixes = []string{"<!-- tf-plan:", "<!-- tf-apply:", "<!-- tfpr:"}

// StripControlLines removes every line that looks like a comment marker or
// record, so text from the plan can never forge one.
func StripControlLines(body string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(body, "\n") {
		trimmed := strings.TrimLeft(line, " \t\v\f\r")
		forged := false
		for _, p := range controlPrefixes {
			forged = forged || strings.HasPrefix(trimmed, p)
		}
		if !forged {
			b.WriteString(line)
		}
	}
	return b.String()
}

func execute(t *template.Template, name string, data any) (string, error) {
	var b strings.Builder
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}
