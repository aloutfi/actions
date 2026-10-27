// Package core holds tfpr's pure logic: plan statistics, naming, the apply
// policies and comment rendering. It performs no I/O; callers pass bytes in and
// get strings or decisions back.
package core

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
)

// Stats counts the changes in a `terraform show -json` plan.
type Stats struct {
	Creates, Updates, Destroys, Replaces, Imports, Forgets, Moves, OutputChanges int
}

// HasChanges reports whether applying the plan changes state. Forgets and
// moves change state too, so they count.
func (s Stats) HasChanges() bool {
	return s.Creates+s.Updates+s.Destroys+s.Replaces+s.Forgets+s.Moves > 0
}

// HasImports reports whether the plan imports any resource.
func (s Stats) HasImports() bool { return s.Imports > 0 }

// HasOutputChanges reports whether any root output changes.
func (s Stats) HasOutputChanges() bool { return s.OutputChanges > 0 }

// Summary is the one-line "N to add, ..." phrase.
func (s Stats) Summary() string {
	out := fmt.Sprintf("%d to add, %d to change, %d to destroy, %d to replace, %d to import",
		s.Creates, s.Updates, s.Destroys, s.Replaces, s.Imports)
	if s.Forgets > 0 {
		out += fmt.Sprintf(", %d to forget", s.Forgets)
	}
	if s.Moves > 0 {
		out += fmt.Sprintf(", %d to move", s.Moves)
	}
	return out
}

// Field is one named plan statistic, as written to a step output.
type Field struct{ Name, Value string }

// Fields lists the statistics in step-output order.
func (s Stats) Fields() []Field {
	n := strconv.Itoa
	b := strconv.FormatBool
	return []Field{
		{"creates", n(s.Creates)},
		{"updates", n(s.Updates)},
		{"destroys", n(s.Destroys)},
		{"replaces", n(s.Replaces)},
		{"imports", n(s.Imports)},
		{"forgets", n(s.Forgets)},
		{"moves", n(s.Moves)},
		{"output_changes", n(s.OutputChanges)},
		{"has_changes", b(s.HasChanges())},
		{"has_imports", b(s.HasImports())},
		{"has_output_changes", b(s.HasOutputChanges())},
		{"summary", s.Summary()},
	}
}

// plan is the subset of `terraform show -json` that PlanStats reads.
// change.actions is ["no-op"|"create"|"read"|"update"|"delete"|"forget"], or
// ["create","delete"] / ["delete","create"] for a replace. change.importing is
// set for imports, previous_address for moves. forget comes from
// removed { lifecycle { destroy = false } }.
type plan struct {
	ResourceChanges []struct {
		Address         string  `json:"address"`
		PreviousAddress *string `json:"previous_address"`
		Change          struct {
			Actions   []string        `json:"actions"`
			Importing json.RawMessage `json:"importing"`
		} `json:"change"`
	} `json:"resource_changes"`
	OutputChanges map[string]struct {
		Actions []string `json:"actions"`
	} `json:"output_changes"`
}

// PlanStats counts the changes in a `terraform show -json` document.
func PlanStats(planJSON []byte) (Stats, error) {
	var p plan
	if err := json.Unmarshal(planJSON, &p); err != nil {
		return Stats{}, fmt.Errorf("parse plan JSON: %w", err)
	}
	var s Stats
	for _, rc := range p.ResourceChanges {
		a := rc.Change.Actions
		create, del := slices.Contains(a, "create"), slices.Contains(a, "delete")
		switch {
		case create && del:
			s.Replaces++
		case create:
			s.Creates++
		case del:
			s.Destroys++
		}
		if slices.Equal(a, []string{"update"}) {
			s.Updates++
		}
		if slices.Contains(a, "forget") {
			s.Forgets++
		}
		if len(rc.Change.Importing) > 0 && string(rc.Change.Importing) != "null" {
			s.Imports++
		}
		if rc.PreviousAddress != nil && *rc.PreviousAddress != rc.Address {
			s.Moves++
		}
	}
	for _, oc := range p.OutputChanges {
		if !slices.Equal(oc.Actions, []string{"no-op"}) {
			s.OutputChanges++
		}
	}
	return s, nil
}
