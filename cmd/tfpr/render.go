package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/aloutfi/actions/ci"
	"github.com/aloutfi/actions/core"
	"github.com/aloutfi/actions/redact"
)

// minCommentBody is the smallest body budget tfpr accepts: room for the
// minimal layout (headline, banner, counts, links).
const minCommentBody = 1000

// commentHost is the dialect and limits of the repo host the comment goes
// to: TFPR_VCS, else where the CI says the repo lives, else GitHub.
func (a *app) commentHost() core.Host {
	return core.HostFor(envOr("TFPR_VCS", a.ctx.VCS))
}

// renderPlan renders the plan comment's human-readable body and passes it
// through the filter. The marker and record, tfpr's own control lines, are
// returned separately and never filtered: a filter that rewrites the stack
// name must not break the upsert match or the record.
//
// The whole comment (marker, record and body) fits the host's limit, or
// TFPR_COMMENT_BUDGET characters when that is set and smaller: the host
// rejects longer comments. If the filter lengthens the body past it, the
// minimal layout is posted instead.
func (a *app) renderPlan(r core.PlanResult, texts map[string]string, links core.Links) (body redact.Text, marker, record string, err error) {
	host := a.commentHost()
	limit, err := commentLimit(host)
	if err != nil {
		return body, "", "", err
	}
	marker, record = core.PlanMarker(r.Target.Stack, r.Target.Workspace), core.Record(r)
	head := 0
	if host.HiddenHTML { // the marker and record go above the body, two lines
		head = utf8.RuneCountInString(marker+record) + 2
	}
	if limit < head+minCommentBody {
		return body, "", "", fmt.Errorf("TFPR_COMMENT_BUDGET is %d characters; this comment needs at least %d (%d for its marker and record, %d for a minimal body)",
			limit, head+minCommentBody, head, minCommentBody)
	}
	tmpl, err := userTemplate()
	if err != nil {
		return body, "", "", err
	}
	c := core.PlanComment{
		Result: r, Texts: texts, Host: host, Links: links, Template: tmpl,
		Budget:        limit - head,
		KeepUnchanged: os.Getenv("TFPR_KEEP_UNCHANGED") == "true",
	}
	body, size, err := a.renderFiltered(c, core.RenderPlan, utf8.RuneCountInString)
	if err == nil && size > c.Budget {
		err = fmt.Errorf("the filtered plan comment is %d characters, over its %d-character budget", size, c.Budget)
	}
	if err != nil {
		return body, "", "", err
	}
	return body, marker, record, nil
}

// commentLimit is the most characters a whole comment may have: the host's
// limit, or TFPR_COMMENT_BUDGET when that is set and smaller.
func commentLimit(host core.Host) (int, error) {
	limit := host.MaxComment
	if b := os.Getenv("TFPR_COMMENT_BUDGET"); b != "" {
		n, err := strconv.Atoi(b)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("TFPR_COMMENT_BUDGET must be a number of characters, got '%s'", b)
		}
		limit = min(n, host.MaxComment)
	}
	return limit, nil
}

// renderFiltered renders c and passes it through the filter. If the result
// measures over c.Budget, it renders and filters the minimal layout once
// instead. It returns the filtered text and its size, which may still be
// over budget.
func (a *app) renderFiltered(c core.PlanComment, render func(core.PlanComment) (string, error), measure func(string) int) (redact.Text, int, error) {
	for {
		raw, err := render(c)
		if err != nil {
			return redact.Text{}, 0, err
		}
		text, err := a.filter.Apply(raw)
		if err != nil {
			return text, 0, err
		}
		if n := measure(text.String()); n <= c.Budget || c.Minimal {
			return text, n, nil
		}
		c.Minimal = true
	}
}

// userTemplate is the text of the file TFPR_TEMPLATE names, or "".
func userTemplate() (string, error) {
	path := os.Getenv("TFPR_TEMPLATE")
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("TFPR_TEMPLATE: %w", err)
	}
	return string(data), nil
}

// writeSummary publishes the full filtered plan as the job summary
// (unless TFPR_SUMMARY=false) and reports whether it did; a summary over
// the CI's limit even in the minimal layout is not written. A redaction
// failure is returned as is, and nothing is published; any other failure is
// only logged, since the summary is a convenience.
func (a *app) writeSummary(r core.PlanResult, texts map[string]string, links core.Links) (bool, error) {
	if os.Getenv("TFPR_SUMMARY") == "false" {
		return false, nil
	}
	host := core.HostFor(a.ctx.CI)
	c := core.PlanComment{Result: r, Texts: texts, Host: host, Links: links, Budget: host.MaxSummary}
	text, size, err := a.renderFiltered(c, core.RenderSummary, func(s string) int { return len(s) })
	switch {
	case errors.Is(err, redact.ErrFailed):
		return false, err
	case err != nil:
		a.ci.Log(ci.Warning, "Could not render the job summary: "+err.Error())
		return false, nil
	case size > host.MaxSummary:
		a.ci.Log(ci.Warning, fmt.Sprintf("Not writing the job summary: even its minimal layout is %d bytes after the filter, over the %d-byte limit.",
			size, host.MaxSummary))
		return false, nil
	}
	if err := a.ci.UploadSummary(text); err != nil {
		a.ci.Log(ci.Warning, "Could not write the job summary: "+err.Error())
		return false, nil
	}
	return true, nil
}

// summaryURL links to the job summary, if the CI gives it a URL.
func (a *app) summaryURL() string {
	if l, ok := a.ci.(ci.SummaryLinker); ok {
		return l.SummaryURL()
	}
	return ""
}
