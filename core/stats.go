// Package core holds tfpr's pure logic: the PlanResult contract and its
// derivation, naming, the apply policies and comment rendering. It performs no
// I/O; callers pass bytes in and get values back.
package core

import (
	"fmt"
	"strconv"
)

// Stats are the plan step's change statistics, as its step outputs and the
// current comment layout show them.
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

// StatsOf maps PlanResult counts onto the plan step's statistics.
func StatsOf(c Counts) Stats {
	return Stats{
		Creates: c.Create, Updates: c.Update, Destroys: c.Delete, Replaces: c.Replace,
		Imports: c.Import, Forgets: c.Forget, Moves: c.Move, OutputChanges: c.Output,
	}
}
