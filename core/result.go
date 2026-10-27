package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// PlanResultSchema identifies plan-result.json; schema/plan-result.v1.json
// is its JSON Schema.
const PlanResultSchema = "tfpr/plan-result@1"

// Unit statuses.
const (
	StatusNoOp        = "no_op"
	StatusChanges     = "changes"
	StatusStateOnly   = "state_only"
	StatusOutputsOnly = "outputs_only"
	StatusError       = "error"
	StatusNotRun      = "not_run"
	StatusExcluded    = "excluded"
)

// PlanResult is the contract between plan (or derive) and every later
// stage. It describes a run of N units (N = 1 for plain terraform, at path
// "."). It never carries attribute values.
type PlanResult struct {
	Schema    string    `json:"schema"`
	Tool      Tool      `json:"tool"`
	Target    Target    `json:"target"`
	Source    Source    `json:"source"`
	Run       Run       `json:"run"`
	Summary   Summary   `json:"summary"`
	Units     []Unit    `json:"units"`
	Planset   *Planset  `json:"planset,omitempty"`
	Redaction Redaction `json:"redaction"`
}

// Tool describes what produced the result.
type Tool struct {
	Version           string `json:"version"`                  // tfpr version
	Runner            string `json:"runner"`                   // terraform | byo
	Binary            string `json:"binary,omitempty"`         // basename of TFPR_TF_BIN
	BinaryVersion     string `json:"binary_version,omitempty"` // terraform version
	PlanFormatVersion string `json:"plan_format_version,omitempty"`
}

// Target is what was planned: one PR comment and one artifact per Key.
type Target struct {
	Stack     string `json:"stack"`
	Workspace string `json:"workspace"`
	Key       string `json:"key"`
}

// Source is the commit that was planned.
type Source struct {
	Repo string  `json:"repo,omitempty"`
	SHA  string  `json:"sha"`
	Tree string  `json:"tree"` // git tree of SHA; "" when unknown
	Ref  string  `json:"ref,omitempty"`
	PR   *string `json:"pr"`
}

// Run is the CI run that planned.
type Run struct {
	CI      string `json:"ci,omitempty"`
	ID      string `json:"id,omitempty"`
	Attempt int    `json:"attempt"` // 0 = the CI has no attempt counter
	URL     string `json:"url,omitempty"`
}

// Summary folds the units.
type Summary struct {
	ExitCode int        `json:"exit_code"`
	Status   string     `json:"status"`
	Counts   Counts     `json:"counts"`
	Units    UnitCounts `json:"units"`
}

// Counts are change counts. Forgets, moves, imports, reads and output
// changes are counted too.
type Counts struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Delete  int `json:"delete"`
	Replace int `json:"replace"`
	Import  int `json:"import"`
	Forget  int `json:"forget"`
	Move    int `json:"move"`
	Read    int `json:"read"`
	Output  int `json:"output"`
}

// UnitCounts counts units by outcome.
type UnitCounts struct {
	Total       int `json:"total"`
	Changed     int `json:"changed"`
	Error       int `json:"error"`
	NotRun      int `json:"not_run"`
	Provisional int `json:"provisional"`
	ApplyNeeded int `json:"apply_needed"`
}

// Unit is one terraform root module's plan.
type Unit struct {
	Path         string     `json:"path"`
	ExitCode     *int       `json:"exit_code"` // nil when the caller did not say
	Status       string     `json:"status"`
	Counts       Counts     `json:"counts"`
	Resources    []Resource `json:"resources"`
	Outputs      []Output   `json:"outputs"`
	Drift        int        `json:"drift"`
	TextChars    *int       `json:"text_chars"`
	Fingerprint  string     `json:"fingerprint"`
	Planfile     Planfile   `json:"planfile"`
	Dependencies []string   `json:"dependencies"`
	StateEmpty   bool       `json:"state_empty"`
	Provisional  bool       `json:"provisional"`
	Skippable    bool       `json:"skippable"`
	Error        string     `json:"error,omitempty"` // why a failed unit has no plan
}

// Resource is one resource change, without values.
type Resource struct {
	Address         string `json:"address"`
	PreviousAddress string `json:"previous_address,omitempty"`
	Action          string `json:"action"`
	Importing       bool   `json:"importing,omitempty"`
	Moved           bool   `json:"moved,omitempty"`
	Forget          bool   `json:"forget,omitempty"` // the old object is forgotten, not destroyed
	Reason          string `json:"reason,omitempty"`
	Mode            string `json:"mode,omitempty"`
}

// Output is one root output change, without its value.
type Output struct {
	Name      string `json:"name"`
	Action    string `json:"action"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// Planfile identifies a unit's binary planfile.
type Planfile struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
}

// Planset pins every unit's planfile with one hash.
type Planset struct {
	SHA256 string `json:"sha256"`
}

// Redaction records whether TFPR_FILTER ran over the text: none, filter or
// byo (caller-supplied text and no filter).
type Redaction struct {
	Mode string `json:"mode"`
}

// UnitInput is what DeriveUnit needs for one unit.
type UnitInput struct {
	Path           string
	PlanJSON       []byte // terraform show -json; read for structure only
	PlanText       string // terraform show -no-color, already filtered
	ExitCode       *int   // terraform plan -detailed-exitcode, if known
	PlanfileSHA256 string // "" when there is no planfile
}

// ErrPlanJSON wraps every plan.json validation failure.
var ErrPlanJSON = errors.New("plan.json is not the output of `terraform show -json`")

// planJSON is the structure DeriveUnit reads. It deliberately has no field
// for any value (before, after, variables, prior state values): values are
// never decoded, so they cannot leak into the result.
type planJSON struct {
	FormatVersion   string `json:"format_version"`
	ResourceChanges []struct {
		Address         string `json:"address"`
		PreviousAddress string `json:"previous_address"`
		Mode            string `json:"mode"`
		ActionReason    string `json:"action_reason"`
		Change          struct {
			Actions   []string        `json:"actions"`
			Importing json.RawMessage `json:"importing"`
		} `json:"change"`
	} `json:"resource_changes"`
	ResourceDrift []json.RawMessage `json:"resource_drift"`
	OutputChanges map[string]struct {
		Actions         []string        `json:"actions"`
		BeforeSensitive json.RawMessage `json:"before_sensitive"`
		AfterSensitive  json.RawMessage `json:"after_sensitive"`
	} `json:"output_changes"`
	PriorState *struct {
		Values *struct {
			RootModule stateModule `json:"root_module"`
		} `json:"values"`
	} `json:"prior_state"`
}

type stateModule struct {
	Resources    []json.RawMessage `json:"resources"`
	ChildModules []stateModule     `json:"child_modules"`
}

func (m stateModule) empty() bool {
	if len(m.Resources) > 0 {
		return false
	}
	for _, c := range m.ChildModules {
		if !c.empty() {
			return false
		}
	}
	return true
}

// parsePlanJSON strictly parses one plan JSON document and fails closed on
// anything else, such as stderr mixed into stdout or a line-based scrubber
// that broke the JSON.
func parsePlanJSON(data []byte) (planJSON, error) {
	var p planJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("%w: %v (stderr merged into stdout, or a scrubber that rewrote the JSON?)", ErrPlanJSON, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return p, fmt.Errorf("%w: unexpected data after the JSON document (stderr merged into stdout?)", ErrPlanJSON)
	}
	if p.FormatVersion == "" {
		return p, fmt.Errorf("%w: no format_version", ErrPlanJSON)
	}
	return p, nil
}

// DeriveUnit derives one unit from its plan JSON and filtered plan text.
// Provisional and Dependencies are left for a multi-unit runner to fill in.
func DeriveUnit(in UnitInput) (Unit, string, error) {
	p, err := parsePlanJSON(in.PlanJSON)
	if err != nil {
		return Unit{}, "", err
	}
	u := Unit{
		Path:         in.Path,
		ExitCode:     in.ExitCode,
		Resources:    []Resource{},
		Outputs:      []Output{},
		Drift:        len(p.ResourceDrift),
		Dependencies: []string{},
		StateEmpty:   p.PriorState == nil || p.PriorState.Values == nil || p.PriorState.Values.RootModule.empty(),
		Fingerprint:  Fingerprint(in.PlanText),
	}
	chars := utf8.RuneCountInString(in.PlanText)
	u.TextChars = &chars
	if in.PlanfileSHA256 != "" {
		u.Planfile = Planfile{Present: true, SHA256: in.PlanfileSHA256}
	}

	for _, rc := range p.ResourceChanges {
		moved := rc.PreviousAddress != "" && rc.PreviousAddress != rc.Address
		importing := len(rc.Change.Importing) > 0 && string(rc.Change.Importing) != "null"
		action := changeAction(rc.Change.Actions)
		switch {
		case action != "":
		case moved:
			action = "move"
		case importing:
			action = "import"
		default:
			continue // no-op
		}
		u.Counts.add(action)
		// ["forget","create"] (or ["create","forget"]): a replacement whose old
		// object is forgotten (lifecycle destroy = false). It is a create and a
		// forget.
		forget := action != "forget" && slices.Contains(rc.Change.Actions, "forget")
		if forget {
			u.Counts.Forget++
		}
		if importing {
			u.Counts.Import++
		}
		if moved {
			u.Counts.Move++
		}
		r := Resource{Address: rc.Address, Action: action, Importing: importing, Moved: moved, Forget: forget, Reason: rc.ActionReason, Mode: rc.Mode}
		if moved {
			r.PreviousAddress = rc.PreviousAddress
		}
		u.Resources = append(u.Resources, r)
	}

	names := make([]string, 0, len(p.OutputChanges))
	for name := range p.OutputChanges {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		oc := p.OutputChanges[name]
		if slices.Equal(oc.Actions, []string{"no-op"}) {
			continue
		}
		u.Counts.Output++
		action := changeAction(oc.Actions)
		if action == "" {
			action = strings.Join(oc.Actions, ",")
		}
		u.Outputs = append(u.Outputs, Output{
			Name:      name,
			Action:    action,
			Sensitive: string(oc.BeforeSensitive) == "true" || string(oc.AfterSensitive) == "true",
		})
	}

	u.Status = unitStatus(u.Counts, u.ExitCode)
	u.Skippable = u.Status == StatusNoOp && !u.Provisional
	return u, p.FormatVersion, nil
}

// FailedUnit is a unit whose plan failed or whose plan output is missing.
// Its status is error, so the summary exits 1 and the comment says the plan
// failed instead of leaving an older plan standing.
func FailedUnit(path string, exit *int, reason string) Unit {
	return Unit{
		Path: path, ExitCode: exit, Status: StatusError, Error: reason,
		Resources: []Resource{}, Outputs: []Output{}, Dependencies: []string{},
	}
}

// changeAction maps change.actions to a resource action, or "" for no-op.
func changeAction(actions []string) string {
	create, del := slices.Contains(actions, "create"), slices.Contains(actions, "delete")
	switch {
	case create && del:
		return "replace"
	case create:
		return "create"
	case del:
		return "delete"
	case slices.Equal(actions, []string{"update"}):
		return "update"
	case slices.Contains(actions, "forget"):
		return "forget"
	case slices.Equal(actions, []string{"read"}):
		return "read"
	}
	return ""
}

func (c *Counts) add(action string) {
	switch action {
	case "create":
		c.Create++
	case "update":
		c.Update++
	case "delete":
		c.Delete++
	case "replace":
		c.Replace++
	case "forget":
		c.Forget++
	case "read":
		c.Read++
	}
}

func (c *Counts) plus(o Counts) {
	c.Create += o.Create
	c.Update += o.Update
	c.Delete += o.Delete
	c.Replace += o.Replace
	c.Import += o.Import
	c.Forget += o.Forget
	c.Move += o.Move
	c.Read += o.Read
	c.Output += o.Output
}

// unitStatus applies the status precedence. A plan that exited 2 is never
// no_op, even when tfpr counts nothing: terraform saw a difference.
func unitStatus(c Counts, exit *int) string {
	switch {
	case exit != nil && *exit == 1:
		return StatusError
	case c.Create+c.Update+c.Delete+c.Replace > 0:
		return StatusChanges
	case c.Import+c.Forget+c.Move > 0:
		return StatusStateOnly
	case c.Output > 0:
		return StatusOutputsOnly
	case exit != nil && *exit == 2:
		return StatusChanges
	}
	return StatusNoOp
}

var severity = map[string]int{
	StatusNoOp: 0, StatusOutputsOnly: 1, StatusStateOnly: 2, StatusChanges: 3, StatusNotRun: 4, StatusError: 5,
}

// Summarize folds units into the summary.
func Summarize(units []Unit) Summary {
	s := Summary{Status: StatusNoOp}
	s.Units.Total = len(units)
	for _, u := range units {
		if u.Status == StatusExcluded {
			continue // removed from the run: nothing of it counts
		}
		s.Counts.plus(u.Counts)
		if severity[u.Status] > severity[s.Status] {
			s.Status = u.Status
		}
		switch u.Status {
		case StatusError:
			s.Units.Error++
		case StatusNotRun:
			s.Units.NotRun++
		case StatusChanges, StatusStateOnly, StatusOutputsOnly:
			s.Units.Changed++
		}
		if u.Provisional {
			s.Units.Provisional++
		}
		if !u.Skippable {
			s.Units.ApplyNeeded++
		}
	}
	switch {
	case s.Units.Error+s.Units.NotRun > 0:
		s.ExitCode = 1
	case s.Status != StatusNoOp:
		s.ExitCode = 2
	}
	return s
}

// NewPlanResult assembles a result: units sorted by path, the summary, and
// the planset over the units' planfiles. Tool, Target, Source, Run and
// Redaction come from the caller.
func NewPlanResult(r PlanResult, units []Unit) PlanResult {
	r.Schema = PlanResultSchema
	r.Units = slices.Clone(units)
	sort.Slice(r.Units, func(i, j int) bool { return r.Units[i].Path < r.Units[j].Path })
	r.Summary = Summarize(r.Units)
	r.Target.Key = Key(r.Target.Stack, r.Target.Workspace)
	var lines []string
	for _, u := range r.Units {
		if u.Planfile.Present {
			lines = append(lines, u.Path+" "+u.Planfile.SHA256)
		}
	}
	r.Planset = nil
	if len(lines) > 0 {
		r.Planset = &Planset{SHA256: sha256Hex(strings.Join(lines, "\n") + "\n")}
	}
	return r
}

// Key is the target key: slug(stack):workspace.
func Key(stack, workspace string) string { return slug(stack) + ":" + orDefault(workspace) }

// Fingerprint is "sha256:<hex>" of text.
func Fingerprint(text string) string { return "sha256:" + sha256Hex(text) }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// recordLimit is the longest record line before "apply" collapses to "all".
const recordLimit = 8000

// Record is the comment record: one hidden line that a later stage (the
// merge-time gate) reads back from the bot's own comment.
func Record(r PlanResult) string {
	apply, provisional, fps := []string{}, []string{}, make([]string, 0, len(r.Units))
	for _, u := range r.Units {
		if u.Status != StatusExcluded && !u.Skippable {
			apply = append(apply, u.Path)
		}
		if u.Provisional {
			provisional = append(provisional, u.Path)
		}
		fps = append(fps, u.Path+" "+u.Fingerprint)
	}
	planset := ""
	if r.Planset != nil {
		planset = "sha256:" + r.Planset.SHA256
	}
	rec := struct {
		Key         string   `json:"key"`
		SHA         string   `json:"sha"`
		Tree        string   `json:"tree"`
		Exit        int      `json:"exit"`
		Status      string   `json:"status"`
		Units       int      `json:"units"`
		Apply       any      `json:"apply"`
		Provisional []string `json:"provisional"`
		Planset     string   `json:"planset"`
		FP          string   `json:"fp"`
		Run         string   `json:"run"`
		Attempt     int      `json:"attempt"`
	}{r.Target.Key, r.Source.SHA, r.Source.Tree, r.Summary.ExitCode, r.Summary.Status, len(r.Units),
		apply, provisional, planset, Fingerprint(strings.Join(fps, "\n") + "\n"), r.Run.ID, r.Run.Attempt}
	line := recordLine(rec)
	if len(line) > recordLimit {
		rec.Apply = "all" // fails open: the gate applies every unit
		line = recordLine(rec)
	}
	return line
}

// recordLine marshals with HTML escaping on, so no "-->" can end the comment early.
func recordLine(rec any) string {
	data, _ := json.Marshal(rec)
	return "<!-- tfpr:plan@1 " + string(data) + " -->"
}
