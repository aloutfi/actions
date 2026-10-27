package core

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// validate checks v against the subset of JSON Schema that
// schema/plan-result.v1.json uses: $ref (local), type, const, enum,
// required, properties, additionalProperties, items and minimum. It is a
// guard that the Go types and the published schema agree, not a general
// validator.
func validate(root, s map[string]any, v any, at string) []string {
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		return validate(root, root["$defs"].(map[string]any)[name].(map[string]any), v, at)
	}
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, at+": "+fmt.Sprintf(format, args...)) }
	if t, ok := s["type"]; ok && !typeOK(t, v) {
		fail("type %v, got %T", t, v)
		return errs
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(c, v) {
		fail("want %v, got %v", c, v)
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range e {
			found = found || reflect.DeepEqual(x, v)
		}
		if !found {
			fail("%v not in %v", v, e)
		}
	}
	if m, ok := s["minimum"].(float64); ok {
		if n, isNum := v.(float64); isNum && n < m {
			fail("%v below %v", n, m)
		}
	}
	if obj, ok := v.(map[string]any); ok {
		for _, r := range asSlice(s["required"]) {
			if _, ok := obj[r.(string)]; !ok {
				fail("missing %q", r)
			}
		}
		props, _ := s["properties"].(map[string]any)
		for k, val := range obj {
			switch sub, ok := props[k].(map[string]any); {
			case ok:
				errs = append(errs, validate(root, sub, val, at+"."+k)...)
			case s["additionalProperties"] == false:
				fail("unexpected %q", k)
			default:
				if ap, ok := s["additionalProperties"].(map[string]any); ok {
					errs = append(errs, validate(root, ap, val, at+"."+k)...)
				} else if props != nil && at != "$" {
					// Not declared at all: the Go type grew a field the schema lacks.
					fail("%q is not in the schema", k)
				}
			}
		}
	}
	if arr, ok := v.([]any); ok {
		if items, ok := s["items"].(map[string]any); ok {
			for i, x := range arr {
				errs = append(errs, validate(root, items, x, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return errs
}

func typeOK(t, v any) bool {
	for _, name := range asSlice(t) {
		switch name {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "integer":
			if n, ok := v.(float64); ok && n == math.Trunc(n) {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		}
	}
	return false
}

func asSlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case nil:
		return nil
	}
	return []any{v}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

// TestGoldensMatchSchema checks every golden plan-result.json, and a result
// with every optional field set, against the published schema.
func TestGoldensMatchSchema(t *testing.T) {
	schema := readJSON(t, filepath.Join("..", "schema", "plan-result.v1.json"))
	goldens, _ := filepath.Glob(filepath.Join("..", "test", "fixtures", "render", "*", "plan-result.json"))
	if len(goldens) == 0 {
		t.Fatal("no golden plan-result.json files")
	}
	for _, g := range goldens {
		for _, e := range validate(schema, schema, readJSON(t, g), "$") {
			t.Errorf("%s: %s", g, e)
		}
	}

	pr := "42"
	full := NewPlanResult(PlanResult{
		Tool:      Tool{Version: "v", Runner: "terraform", Binary: "terraform", BinaryVersion: "1.16.5", PlanFormatVersion: "1.2"},
		Target:    Target{Stack: "s", Workspace: "w"},
		Source:    Source{Repo: "o/r", SHA: "a", Tree: "b", Ref: "refs/heads/x", PR: &pr},
		Run:       Run{CI: "github", ID: "1", Attempt: 1, URL: "u"},
		Redaction: Redaction{Mode: "filter"},
	}, []Unit{{
		Path: ".", ExitCode: intp(2), Status: StatusChanges, Counts: Counts{Create: 1},
		Resources: []Resource{{Address: "a.b", PreviousAddress: "a.c", Action: "update", Importing: true, Moved: true, Reason: "r", Mode: "managed"}},
		Outputs:   []Output{{Name: "o", Action: "create", Sensitive: true}},
		TextChars: intp(3), Fingerprint: "sha256:x", Planfile: Planfile{Present: true, SHA256: "y"}, Dependencies: []string{},
	}})
	data, _ := json.Marshal(full)
	var v map[string]any
	_ = json.Unmarshal(data, &v)
	for _, e := range validate(schema, schema, v, "$") {
		t.Errorf("full result: %s", e)
	}
}
