package main

import (
	"fmt"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/aloutfi/actions/ci"
	"github.com/aloutfi/actions/core"
	"github.com/aloutfi/actions/redact"
)

// commentHost is the dialect and limits of the repo host the comment goes to.
func commentHost() core.Host { return core.HostFor(envOr("TFPR_VCS", "github")) }

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
	host := commentHost()
	limit := host.MaxComment
	if b := os.Getenv("TFPR_COMMENT_BUDGET"); b != "" {
		if limit, err = strconv.Atoi(b); err != nil || limit <= 0 {
			return body, "", "", fmt.Errorf("TFPR_COMMENT_BUDGET must be a number of characters, got '%s'", b)
		}
		limit = min(limit, host.MaxComment)
	}
	tmpl, err := userTemplate()
	if err != nil {
		return body, "", "", err
	}
	marker, record = core.PlanMarker(r.Target.Stack, r.Target.Workspace), core.Record(r)
	c := core.PlanComment{
		Result: r, Texts: texts, Host: host, Links: links, Template: tmpl,
		Budget:        limit - utf8.RuneCountInString(marker+record) - 2, // and two newlines
		KeepUnchanged: os.Getenv("TFPR_KEEP_UNCHANGED") == "true",
	}
	for {
		raw, err := core.RenderPlan(c)
		if err != nil {
			return body, "", "", err
		}
		if body, err = a.filter.Apply(raw); err != nil {
			return body, "", "", err
		}
		if n := utf8.RuneCountInString(body.String()); n <= c.Budget || c.Minimal {
			if n > c.Budget {
				return body, "", "", fmt.Errorf("the filtered plan comment is %d characters, over its %d-character budget", n, c.Budget)
			}
			return body, marker, record, nil
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
// (unless TFPR_SUMMARY=false) and reports whether it did. A redaction
// failure is returned as is, and nothing is published; any other failure is
// only logged, since the summary is a convenience.
func (a *app) writeSummary(r core.PlanResult, texts map[string]string, links core.Links) (bool, error) {
	if os.Getenv("TFPR_SUMMARY") == "false" {
		return false, nil
	}
	host := core.HostFor(a.ctx.CI)
	c := core.PlanComment{Result: r, Texts: texts, Host: host, Links: links}
	var text redact.Text
	for {
		raw, err := core.RenderSummary(c)
		if err != nil {
			a.ci.Log(ci.Warning, "Could not render the job summary: "+err.Error())
			return false, nil
		}
		if text, err = a.filter.Apply(raw); err != nil {
			return false, err
		}
		if len(text.String()) <= host.MaxSummary || c.Minimal {
			break
		}
		c.Minimal = true
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
