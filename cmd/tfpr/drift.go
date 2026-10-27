package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/aloutfi/actions/ci"
	"github.com/aloutfi/actions/core"
)

// runMode is TFPR_MODE: "pr" (the default) or "drift".
func runMode() (string, error) {
	switch m := envOr("TFPR_MODE", "pr"); m {
	case "pr", "drift":
		return m, nil
	default:
		return "", fmt.Errorf("TFPR_MODE must be pr or drift, got '%s'", m)
	}
}

// drift reports a scheduled drift check for one stack: the full filtered
// plan goes to the run summary and no PR is touched. If the plan would
// change anything, it sets has_changes=true, warns with the stack's name
// and, on Azure Pipelines, tags the run "drift". With TFPR_FAIL_ON_DRIFT,
// drift ends the step SucceededWithIssues where the CI has that, and fails
// it elsewhere. A failed plan fails.
func (a *app) drift() error {
	r, texts, err := a.readPlanDir()
	if err != nil {
		return fmt.Errorf("drift check of '%s': %w", a.stack, err)
	}
	links := core.Links{Job: a.ci.JobURL(), PlanText: os.Getenv("TFPR_PLAN_TXT_URL")}
	if _, err := a.writeSummary(r, texts, links); err != nil {
		return fmt.Errorf("%w; nothing was published", err)
	}
	if r.Summary.Status == core.StatusError {
		return fmt.Errorf("the drift check of '%s' (%s) failed: the plan did not complete", a.stack, a.workspace)
	}
	drifted := r.Summary.Status != core.StatusNoOp
	if err := a.outputs(core.Field{Name: "has_changes", Value: strconv.FormatBool(drifted)}); err != nil {
		return err
	}
	if !drifted {
		fmt.Fprintf(a.stdout, "No drift in '%s' (%s).\n", a.stack, a.workspace)
		return nil
	}
	msg := fmt.Sprintf("Drift detected in '%s' (%s): %s.", a.stack, a.workspace, core.StatsOf(r.Summary.Counts).Summary())
	a.ci.Log(ci.Warning, msg)
	if t, ok := a.ci.(ci.Tagger); ok {
		if err := t.AddBuildTag("drift"); err != nil {
			a.ci.Log(ci.Warning, "Could not tag the run: "+err.Error())
		}
	}
	if os.Getenv("TFPR_FAIL_ON_DRIFT") != "true" {
		return nil
	}
	if p, ok := a.ci.(ci.PartialSucceeder); ok {
		return p.CompleteWithIssues(msg)
	}
	return errors.New("failing on drift (TFPR_FAIL_ON_DRIFT=true): " + msg)
}
