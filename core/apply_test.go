package core

import (
	"fmt"
	"strings"
	"testing"
)

func report(action, reason string, exit int) ApplyReport {
	return ApplyReport{
		Stack: "infra", Workspace: "default", Host: HostFor("github"),
		Gate:     GateResult{Action: action, Reason: reason, BranchHead: "def4567890"},
		ExitCode: exit, SHA: "abc1234567", JobURL: "https://ci.example/run/1", ToolVersion: "v0.2.0",
	}
}

func TestRenderApplyReport(t *testing.T) {
	for _, c := range []struct {
		name     string
		r        ApplyReport
		resolved bool
		want     []string
	}{
		{"applied", report(GateApply, "", 0), true, []string{"### ✅ Terraform apply · `infra` (`default`) · succeeded", "Commit `abc1234` · [job log](https://ci.example/run/1) · tfpr v0.2.0"}},
		{"failed", report(GateApply, "", 3), false, []string{"❌ Terraform apply · `infra` (`default`) · failed (exit 3)"}},
		{"superseded", func() ApplyReport {
			r := report(GateSkip, "superseded", 0)
			r.SupersededBy = "https://ci.example/run/2"
			return r
		}(), true, []string{"skipped: superseded", "> **Superseded by `def4567`**; that commit's run applies these changes ([link](https://ci.example/run/2))."}},
		{"no-op", report(GateSkip, "no-op", 0), true, []string{"skipped: no changes", "> **Apply skipped**: the reviewed plan had no changes"}},
		{"refused", report(GateRefuse, "unverified-ref", 0), false, []string{"🛑 Terraform apply", "refused", "> **Apply refused**: tfpr could not verify that `abc1234`"}},
		{"planfile source", func() ApplyReport {
			r := report(GateApply, "", 0)
			r.Source, r.PlanRun = Decision{Source: SourcePlanfile}, "77"
			return r
		}(), true, []string{"Source: planfile from PR review (run #77)"}},
	} {
		got, err := RenderApplyReport(c.r)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in\n%s", c.name, w, got)
			}
		}
		if c.r.Resolved() != c.resolved {
			t.Errorf("%s: resolved %v", c.name, c.r.Resolved())
		}
		if strings.Contains(got, "<!--") {
			t.Errorf("%s: control line in the body", c.name)
		}
	}
}

// A long apply log is cut to the budget, keeping its end, under a title that
// says so; the Azure dialect uses a heading, not <details>.
func TestApplyReportLog(t *testing.T) {
	var lines []string
	for i := 1; i <= 2000; i++ {
		lines = append(lines, fmt.Sprintf("module.x.resource[%d]: Creating...", i))
	}
	lines = append(lines, "Apply complete! Resources: 2000 added, 0 changed, 0 destroyed.")
	for _, host := range []string{"github", "azure"} {
		r := report(GateApply, "", 0)
		r.Host, r.Log, r.Budget = HostFor(host), strings.Join(lines, "\n")+"\n", 4000
		got, err := RenderApplyReport(r)
		if err != nil {
			t.Fatal(err)
		}
		if n := inRunes.of(got); n > 4000 {
			t.Errorf("%s: %d characters, over the budget", host, n)
		}
		if !strings.Contains(got, "Apply complete! Resources: 2000 added") || strings.Contains(got, "resource[1]:") {
			t.Errorf("%s: the log's end was not kept:\n%s", host, got)
		}
		if !strings.Contains(got, "Apply output (last ") {
			t.Errorf("%s: the title doesn't say the log was cut", host)
		}
		if strings.Contains(got, "<details") != (host == "github") {
			t.Errorf("%s: wrong dialect:\n%.300s", host, got)
		}
	}
	// A short log is shown whole, open when the apply failed.
	r := report(GateApply, "", 1)
	r.Log = "Error: boom\n"
	got, _ := RenderApplyReport(r)
	if !strings.Contains(got, "<details open><summary>Apply output</summary>") || !strings.Contains(got, "Error: boom") {
		t.Errorf("short log:\n%s", got)
	}
}

func TestCutTail(t *testing.T) {
	if got := inRunes.cutTail("a\nb\nc\n", 4); got != "b\nc\n" {
		t.Errorf("lines: %q", got)
	}
	if got := inRunes.cutTail("short", 10); got != "short" {
		t.Errorf("fits: %q", got)
	}
	if got := inRunes.cutTail("one very long line", 4); got != "line" {
		t.Errorf("characters: %q", got)
	}
	if got := inBytes.cutTail("xé", 2); got != "é" {
		t.Errorf("bytes: %q", got)
	}
}
