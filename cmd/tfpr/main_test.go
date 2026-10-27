package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// fakeGitHub is the GitHub REST API as tfpr uses it. Its fields shape the
// responses; calls and the last written comment body are recorded.
type fakeGitHub struct {
	t          *testing.T
	pr         string // PR number associated with every commit; "" for none
	branchHead string
	planRun    string
	failWrites bool
	failList   bool   // listing comments fails
	comments   string // JSON for the PR's existing comments

	mu    sync.Mutex
	calls []string
	body  string
	auth  string // last Authorization header
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	f.auth = r.Header.Get("Authorization")
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost || r.Method == http.MethodPatch:
		if f.failWrites {
			http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
			return
		}
		var req struct{ Body string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("comment body: %v", err)
		}
		f.body = req.Body
		io.WriteString(w, `{"id":1}`)
	case strings.HasSuffix(p, "/jobs"):
		io.WriteString(w, `{"jobs":[{"id":7,"runner_name":"other"},{"id":8,"runner_name":null}]}`)
	case strings.HasSuffix(p, "/comments"):
		if f.failList {
			http.Error(w, `{"message":"boom"}`, http.StatusNotFound)
			return
		}
		io.WriteString(w, f.comments)
	case strings.HasSuffix(p, "/pulls"):
		if f.pr == "" {
			io.WriteString(w, `[]`)
		} else {
			io.WriteString(w, `[{"number":`+f.pr+`,"head":{"sha":"headsha"}}]`)
		}
	case strings.Contains(p, "/git/ref/heads/"):
		io.WriteString(w, `{"object":{"sha":"`+f.branchHead+`"}}`)
	case strings.Contains(p, "/actions/workflows/"):
		if f.planRun == "" {
			io.WriteString(w, `{"workflow_runs":[]}`)
		} else {
			io.WriteString(w, `{"workflow_runs":[{"id":`+f.planRun+`}]}`)
		}
	default:
		f.t.Errorf("fake GitHub: unexpected call %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

// stubTerraform logs each call to $FAKE_LOG and its environment to
// $FAKE_LOG.env; plan and apply exit with $FAKE_TF_PLAN_CODE and
// $FAKE_TF_APPLY_CODE; show prints the plan from $FAKE_TF_PLAN (a fixture
// directory).
const stubTerraform = `#!/bin/sh
echo "terraform $*" >> "$FAKE_LOG"
env > "$FAKE_LOG.env"
case "$1" in
  version) echo '{"terraform_version":"0.0.0"}' ;;
  plan) echo "binary plan" > tfplan; echo "  # terraform_data.secret-key will be created"
        [ -z "$FAKE_TF_SAY" ] || echo "$FAKE_TF_SAY"; exit "${FAKE_TF_PLAN_CODE:-0}" ;;
  show) if [ "$2" = "-json" ]; then cat "$FAKE_TF_PLAN/plan.json"; else cat "$FAKE_TF_PLAN/plan.txt"; fi ;;
  apply) [ -z "$FAKE_TF_SAY" ] || echo "$FAKE_TF_SAY"; exit "${FAKE_TF_APPLY_CODE:-0}" ;;
esac
`

// scenario is one tfpr run in an empty workspace with a stack/ directory, as
// a first-attempt push build of refs/heads/main at sha "abc", which is also
// the branch head and belongs to PR 42.
type scenario struct {
	t   *testing.T
	ws  string
	gh  *fakeGitHub
	env map[string]string
}

// result is what a scenario run produced.
type result struct {
	rc      int
	out     string // stdout and stderr
	outputs string // step outputs
	tf      string // terraform calls
	api     string // GitHub API calls
	body    string // last comment body written
	summary string // the job summary written
}

func newScenario(t *testing.T) *scenario {
	if runtime.GOOS == "windows" {
		t.Skip("the terraform stub is a shell script")
	}
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	must(t, os.MkdirAll(filepath.Join(ws, "stack"), 0o755))
	stub := filepath.Join(dir, "terraform")
	must(t, os.WriteFile(stub, []byte(stubTerraform), 0o755))
	event := filepath.Join(dir, "event.json")
	must(t, os.WriteFile(event, []byte(`{"pull_request":{"number":42}}`), 0o644))
	gh := &fakeGitHub{t: t, pr: "42", branchHead: "abc", comments: `[]`}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)

	env := map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_OUTPUT": filepath.Join(dir, "outputs"),
		"GITHUB_REPOSITORY": "o/r", "GITHUB_RUN_ID": "1", "GITHUB_RUN_ATTEMPT": "1",
		"GITHUB_SERVER_URL": "https://github.example", "GITHUB_SHA": "abc",
		"GITHUB_REF": "refs/heads/main", "GITHUB_ACTOR": "me", "RUNNER_NAME": "r",
		"GITHUB_EVENT_NAME": "push", "GITHUB_EVENT_PATH": event,
		"GITHUB_API_URL": srv.URL, "TFPR_TF_BIN": stub, "FAKE_LOG": filepath.Join(dir, "log"),
		"GITHUB_STEP_SUMMARY": filepath.Join(dir, "summary"),
		"TFPR_STACK":          "stack", "TFPR_WORKSPACE": "default",
	}
	// Anything the host CI sets must not leak in.
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "TFPR_GITHUB_TOKEN", "TFPR_EXIT_CODE", "TFPR_RUN_ID", "TFPR_JOB_URL",
		"TFPR_COMMENT_BUDGET", "TFPR_COMMENT_AUTHOR", "TFPR_USE_PLANFILE", "TFPR_ALLOW_FRESH_PLAN_FALLBACK",
		"TFPR_PLAN_DIR", "TFPR_PLAN_RUN_ID", "TFPR_LOCK_TIMEOUT", "TFPR_PLAN_WORKFLOW", "TFPR_VCS",
		"TFPR_REPO", "TFPR_OUTPUT_FILE", "TFPR_DIR", "TFPR_FILTER", "TFPR_PLAN_JSON", "TFPR_PLAN_TXT", "TFPR_PLANFILE",
		"TFPR_TEMPLATE", "TFPR_SUMMARY", "TFPR_SUMMARY_URL", "TFPR_SUMMARY_FILE", "TFPR_PLAN_TXT_URL", "TFPR_KEEP_UNCHANGED", "FAKE_TF_PLAN", "FAKE_TF_PLAN_CODE", "FAKE_TF_APPLY_CODE", "FAKE_TF_SAY"} {
		env[k] = ""
	}
	return &scenario{t: t, ws: ws, gh: gh, env: env}
}

func (s *scenario) set(kv ...string) *scenario {
	for i := 0; i+1 < len(kv); i += 2 {
		s.env[kv[i]] = kv[i+1]
	}
	return s
}

// planArtifact fakes a downloaded planfile artifact in ws/artifact.
func (s *scenario) planArtifact(stack, workspace string) {
	dir := filepath.Join(s.ws, "artifact")
	must(s.t, os.MkdirAll(dir, 0o755))
	must(s.t, os.WriteFile(filepath.Join(dir, "tfplan"), []byte("reviewed plan\n"), 0o644))
	meta, _ := json.Marshal(map[string]string{"stack": stack, "workspace": workspace})
	must(s.t, os.WriteFile(filepath.Join(dir, "plan-metadata.json"), meta, 0o644))
}

func (s *scenario) write(name, content string) {
	must(s.t, os.WriteFile(filepath.Join(s.ws, name), []byte(content), 0o644))
}

// byo runs `tfpr derive` on a render fixture, as a caller that ran terraform
// itself, and fails the test if it does not succeed.
func (s *scenario) byo(fixture string) result {
	s.t.Helper()
	dir := filepath.Join(root, "test", "fixtures", "render", fixture)
	exit, err := os.ReadFile(filepath.Join(dir, "exit_code"))
	must(s.t, err)
	s.set("TFPR_PLAN_JSON", s.fixtureFile(dir, "plan.json"), "TFPR_PLAN_TXT", s.fixtureFile(dir, "plan.txt"),
		"TFPR_EXIT_CODE", strings.TrimSpace(string(exit)))
	r := s.run("derive")
	if r.rc != 0 {
		s.t.Fatalf("derive failed:\n%s", r.out)
	}
	return r
}

// fixtureFile is the path of a fixture file; a gzipped one (large
// fixtures) is unzipped into the workspace first.
func (s *scenario) fixtureFile(dir, name string) string {
	path := filepath.Join(dir, name)
	f, err := os.Open(path + ".gz")
	if os.IsNotExist(err) {
		return path
	}
	must(s.t, err)
	defer f.Close()
	z, err := gzip.NewReader(f)
	must(s.t, err)
	data, err := io.ReadAll(z)
	must(s.t, err)
	out := filepath.Join(s.ws, "fixture-"+name)
	must(s.t, os.WriteFile(out, data, 0o644))
	return out
}

// planResult reads the plan dir's plan-result.json.
func (s *scenario) planResult() string {
	data, err := os.ReadFile(filepath.Join(s.ws, "stack", ".tfpr", "plan-result.json"))
	must(s.t, err)
	return string(data)
}

func (s *scenario) run(cmd string) result {
	for k, v := range s.env {
		s.t.Setenv(k, v)
	}
	s.t.Chdir(s.ws)
	// Each run starts with empty logs; the fake API keeps its call history.
	must(s.t, os.WriteFile(s.env["GITHUB_OUTPUT"], nil, 0o644))
	must(s.t, os.WriteFile(s.env["FAKE_LOG"], nil, 0o644))
	must(s.t, os.WriteFile(s.env["GITHUB_STEP_SUMMARY"], nil, 0o644))
	var out syncBuffer
	rc := run([]string{cmd}, &out, &out)
	read := func(name string) string {
		data, _ := os.ReadFile(s.env[name])
		return string(data)
	}
	return result{rc: rc, out: out.String(), outputs: read("GITHUB_OUTPUT"), tf: read("FAKE_LOG"),
		api: strings.Join(s.gh.calls, "\n"), body: s.gh.body, summary: read("GITHUB_STEP_SUMMARY")}
}

// syncBuffer collects stdout and stderr together. Filters and terraform
// write to it from several goroutines, which a bare bytes.Buffer would race on.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var root = filepath.Join("..", "..")

func init() {
	// Tests change directory, so anchor the repo root first.
	abs, err := filepath.Abs(root)
	if err != nil {
		panic(err)
	}
	root = abs
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func eq[T comparable](t *testing.T, name string, want, got T) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %v, got %v", name, want, got)
	}
}

func has(t *testing.T, name, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: expected to contain %q in:\n%s", name, needle, haystack)
	}
}

func lacks(t *testing.T, name, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("%s: expected not to contain %q in:\n%s", name, needle, haystack)
	}
}

var update = flag.Bool("update", false, "rewrite the golden files these tests own")

// golden compares got with test/fixtures/<file>, shared with test/run.sh.
// go test ./cmd/tfpr -update rewrites it.
func golden(t *testing.T, file, got string) {
	t.Helper()
	path := filepath.Join(root, "test", "fixtures", file)
	if *update {
		must(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	must(t, err)
	if string(want) != got {
		t.Errorf("%s differs; got:\n%s", file, got)
	}
}

func TestPlan(t *testing.T) {
	s := newScenario(t).set("FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "changes"), "FAKE_TF_PLAN_CODE", "2")
	r := s.run("plan")
	eq(t, "exit code", 0, r.rc)
	has(t, "validates", r.tf, "terraform validate -no-color")
	has(t, "plans", r.tf, "terraform plan -input=false -no-color -detailed-exitcode -out=tfplan")
	golden(t, "plan/outputs", r.outputs)
	meta, err := os.ReadFile(filepath.Join(s.ws, "stack", "plan-metadata.json"))
	must(t, err)
	created := regexp.MustCompile(`"created_at": "\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ"`)
	golden(t, "plan/plan-metadata.json", created.ReplaceAllString(string(meta), `"created_at": "<now>"`))
	txt, err := os.ReadFile(filepath.Join(s.ws, "stack", ".tfpr", "units", "plan.txt"))
	must(t, err)
	want, err := os.ReadFile(filepath.Join(root, "test", "fixtures", "render", "changes", "plan.txt"))
	must(t, err)
	eq(t, "plan.txt", string(want), string(txt))
	res := s.planResult()
	for _, want := range []string{`"runner": "terraform"`, `"sha": "abc"`, `"status": "changes"`, `"mode": "none"`, `"present": true`} {
		has(t, "plan-result.json", res, want)
	}
	_, err = os.Stat(filepath.Join(s.ws, "stack", ".tfpr", "units", "plan.json"))
	eq(t, "plan.json stays out of the plan dir", true, os.IsNotExist(err))
}

func TestPlanError(t *testing.T) {
	r := newScenario(t).set("FAKE_TF_PLAN_CODE", "1").run("plan")
	eq(t, "exit code", 1, r.rc)
	eq(t, "outputs", "exit_code=1\n", r.outputs)
	lacks(t, "no show", r.tf, "terraform show")
}

// A relative TFPR_TF_BIN is relative to where tfpr runs, not to the stack,
// and a bare name is looked up on PATH; every call uses the same executable.
func TestTerraformBinResolution(t *testing.T) {
	s := newScenario(t).set("FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "no-changes"))
	stub, err := os.ReadFile(s.env["TFPR_TF_BIN"])
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(s.ws, "tools"), 0o755))
	must(t, os.WriteFile(filepath.Join(s.ws, "tools", "terraform"), stub, 0o755))

	s.env["TFPR_TF_BIN"] = "tools/terraform"
	r := s.run("plan")
	eq(t, "relative path: exit code", 0, r.rc)
	has(t, "relative path: version lookup", r.tf, "terraform version -json")
	meta, _ := os.ReadFile(filepath.Join(s.ws, "stack", "plan-metadata.json"))
	has(t, "relative path: version recorded", string(meta), `"terraform_version": "0.0.0"`)

	s.env["TFPR_TF_BIN"] = ""
	s.env["PATH"] = filepath.Join(s.ws, "tools") + string(os.PathListSeparator) + os.Getenv("PATH")
	eq(t, "bare name on PATH: exit code", 0, s.run("plan").rc)
}

func TestPlanUsesTerraformBin(t *testing.T) {
	s := newScenario(t)
	s.env["TFPR_TF_BIN"] = filepath.Join(s.ws, "missing-terraform")
	r := s.run("plan")
	eq(t, "exit code", 1, r.rc)
	has(t, "names the executable", r.out, "missing-terraform")
}

// Comment failures are best effort: they warn and keep the command's result.
func TestCommentFailuresAreBestEffort(t *testing.T) {
	s := newScenario(t)
	s.gh.failWrites = true
	r := s.run("apply")
	eq(t, "apply ok: exit code", 0, r.rc)
	has(t, "apply ok: warns", r.out, "::warning::Could not post the apply-result comment")
	has(t, "apply ok: output", r.outputs, "apply_exit_code=0")

	s = newScenario(t).set("FAKE_TF_APPLY_CODE", "3")
	s.gh.failWrites = true
	eq(t, "apply fails: exit code", 3, s.run("apply").rc)

	s = newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_EXIT_CODE", "0")
	s.byo("no-changes")
	s.gh.failWrites = true
	r = s.run("comment")
	eq(t, "plan comment: exit code", 0, r.rc)
	has(t, "plan comment: warns", r.out, "::warning::Could not post the plan comment on PR #42")
}

func TestCommentPosts(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_EXIT_CODE", "0")
	s.byo("no-changes")
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	has(t, "posts", r.api, "POST /repos/o/r/issues/42/comments")
	has(t, "says so", r.out, "Posted comment on PR #42")
	golden(t, "comments/plan.md", r.body)
}

// The stack is cleaned once, so every spelling yields the same marker, label
// and metadata.
func TestStackIsCleaned(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_EXIT_CODE", "0", "TFPR_STACK", "./stack/")
	s.byo("no-changes")
	r := s.run("comment")
	golden(t, "comments/plan.md", r.body)

	s = newScenario(t).set("TFPR_STACK", "./stack/", "FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "no-changes"))
	s.run("plan")
	meta, err := os.ReadFile(filepath.Join(s.ws, "stack", "plan-metadata.json"))
	must(t, err)
	has(t, "metadata stack", string(meta), `"stack": "stack",`)
}

func TestCommentUpdatesOwnComment(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_EXIT_CODE", "0")
	s.byo("no-changes")
	s.gh.comments = `[{"id":5,"user":{"login":"someone"},"body":"<!-- tf-plan:stack :: default -->"},
		{"id":6,"user":{"login":"github-actions[bot]"},"body":"<!-- tf-plan:stack :: default -->\nold"}]`
	r := s.run("comment")
	has(t, "patches its own", r.api, "PATCH /repos/o/r/issues/comments/6")
	has(t, "says so", r.out, "Updated comment 6 on PR #42")
}

// If the existing comments cannot be listed, posting could duplicate one, so
// the comment is skipped with a warning.
func TestCommentSkipsWhenListingFails(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_EXIT_CODE", "0")
	s.byo("no-changes")
	s.gh.failList = true
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	has(t, "warns", r.out, "::warning::Could not post the plan comment on PR #42")
	lacks(t, "does not post", r.api, "POST")
}

func TestCommentSkipsNonPR(t *testing.T) {
	r := newScenario(t).run("comment")
	eq(t, "exit code", 0, r.rc)
	has(t, "message", r.out, "Not a pull request build; skipping plan comment.")
	eq(t, "no API calls", "", r.api)
}

func TestRerunApplyFailedComment(t *testing.T) {
	r := newScenario(t).set("FAKE_TF_APPLY_CODE", "3", "GITHUB_RUN_ATTEMPT", "2",
		"TFPR_USE_PLANFILE", "true", "TFPR_PLAN_RUN_ID", "77").run("apply")
	eq(t, "exit code", 3, r.rc)
	golden(t, "comments/apply-rerun-failed.md", r.body)
}

// A missing artifact fails closed and points at retention and the opt-in fallback.
func TestApplyMissingArtifact(t *testing.T) {
	r := newScenario(t).set("TFPR_USE_PLANFILE", "true").run("apply")
	eq(t, "exit code", 1, r.rc)
	has(t, "expiry hint", r.out, "may have expired")
	has(t, "names retention input", r.out, "retention_days")
	has(t, "names fallback input", r.out, "allow_fresh_plan_fallback=true")
	lacks(t, "no apply", r.tf, "terraform apply")
}

// Planfile provenance: only a downloaded planfile for this stack/workspace counts.
func TestApplyPlanfileProvenance(t *testing.T) {
	s := newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.write("stack/tfplan", "local plan\n")
	r := s.run("apply")
	eq(t, "stack tfplan without artifact: exit code", 1, r.rc)
	lacks(t, "stack tfplan without artifact: no apply", r.tf, "terraform apply")

	s = newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.write("stack/tfplan", "local plan\n")
	s.planArtifact("stack", "default")
	r = s.run("apply")
	eq(t, "matching artifact: exit code", 0, r.rc)
	has(t, "matching artifact: applies planfile", r.tf, "terraform apply -input=false -no-color -lock-timeout=5m tfplan")
	lacks(t, "matching artifact: no -auto-approve", r.tf, "-auto-approve")
	got, _ := os.ReadFile(filepath.Join(s.ws, "stack", "tfplan"))
	eq(t, "matching artifact: reviewed plan copied over local one", "reviewed plan\n", string(got))
	has(t, "matching artifact: output", r.outputs, "used_plan_artifact=true")
	golden(t, "comments/apply-planfile.md", r.body)

	for _, c := range []struct{ name, stack, workspace string }{
		{"another stack", "other-stack", "default"},
		{"another workspace", "stack", "prod"},
	} {
		s = newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
		s.planArtifact(c.stack, c.workspace)
		r = s.run("apply")
		eq(t, c.name+": exit code", 1, r.rc)
		has(t, c.name+": warns", r.out, "::warning::Ignoring the planfile artifact")
		lacks(t, c.name+": no apply", r.tf, "terraform apply")
	}

	s = newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.planArtifact("stack", "default")
	must(t, os.Remove(filepath.Join(s.ws, "artifact", "plan-metadata.json")))
	eq(t, "artifact without metadata: exit code", 1, s.run("apply").rc)

	s = newScenario(t)
	s.write("stack/tfplan", "local plan\n")
	has(t, "use_planfile=false ignores stack tfplan", s.run("apply").tf, "-lock-timeout=5m -auto-approve")
}

// The metadata's stack and TFPR_STACK match as paths, not strings.
func TestApplyPlanfileStackPathsMatch(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root, "test", "cases", "same_stack.tsv"))
	must(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		c := strings.Split(line, "\t")
		if strings.HasPrefix(line, "#") {
			continue
		}
		s := newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact", "TFPR_STACK", c[2])
		s.planArtifact(c[1], "default")
		r := s.run("apply")
		if c[3] == "true" {
			eq(t, c[0]+": exit code", 0, r.rc)
			has(t, c[0]+": applies the planfile", r.tf, "-lock-timeout=5m tfplan")
		} else {
			eq(t, c[0]+": exit code", 1, r.rc)
			lacks(t, c[0]+": no apply", r.tf, "terraform apply")
		}
	}
}

func TestDecide(t *testing.T) {
	s := newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.planArtifact("stack", "default")
	r := s.run("decide")
	_, err := os.Stat(filepath.Join(s.ws, "artifact"))
	eq(t, "empties the artifact dir", true, os.IsNotExist(err))
	eq(t, "outputs", "download=true\nartifact_name=tfplan-stack-default\n", r.outputs)

	r = newScenario(t).set("TFPR_USE_PLANFILE", "true", "GITHUB_RUN_ATTEMPT", "2").run("decide")
	has(t, "re-run: no download", r.outputs, "download=false")
}

// Superseded-tree guard: every fresh apply must be the branch head.
func TestApplySupersededGuard(t *testing.T) {
	r := newScenario(t).run("apply")
	eq(t, "branch head: exit code", 0, r.rc)
	has(t, "branch head: checks head", r.api, "GET /repos/o/r/git/ref/heads/main")
	has(t, "branch head: applies", r.tf, "-auto-approve")
	has(t, "branch head: says so", r.out, "Superseded-tree check passed: abc is the head of 'main'.")

	s := newScenario(t)
	s.gh.branchHead = "def"
	r = s.run("apply")
	eq(t, "branch moved: refused", 1, r.rc)
	has(t, "branch moved: error", r.out, "::error::Fresh apply refused (attempt=1): commit abc is no longer the head of 'main' (now def)")
	lacks(t, "branch moved: no apply", r.tf, "terraform apply")
	has(t, "branch moved: reason", r.outputs, "fallback_reason=superseded")
	golden(t, "comments/apply-superseded.md", r.body)

	r = newScenario(t).set("GITHUB_RUN_ATTEMPT", "2", "TFPR_USE_PLANFILE", "true").run("apply")
	eq(t, "re-run at head: exit code", 0, r.rc)
	has(t, "re-run at head: fresh apply", r.tf, "-auto-approve")
	has(t, "re-run at head: reason", r.outputs, "fallback_reason=rerun")

	s = newScenario(t).set("GITHUB_RUN_ATTEMPT", "2")
	s.gh.branchHead = "def"
	r = s.run("apply")
	eq(t, "re-run, branch moved: refused", 1, r.rc)
	lacks(t, "re-run, branch moved: no apply", r.tf, "terraform apply")

	r = newScenario(t).set("GITHUB_REF", "refs/tags/v1").run("apply")
	eq(t, "non-branch first attempt: allowed", 0, r.rc)
	has(t, "non-branch first attempt: warns", r.out, "::warning::Fresh apply of non-branch ref 'refs/tags/v1'")
	has(t, "non-branch first attempt: applies", r.tf, "-auto-approve")
	lacks(t, "non-branch first attempt: no head lookup", r.api, "git/ref/heads")

	r = newScenario(t).set("GITHUB_REF", "refs/tags/v1", "GITHUB_RUN_ATTEMPT", "2").run("apply")
	eq(t, "non-branch re-run: refused", 1, r.rc)
	has(t, "non-branch re-run: reason", r.outputs, "fallback_reason=unverified-ref")
	lacks(t, "non-branch re-run: no apply", r.tf, "terraform apply")

	s = newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.planArtifact("stack", "default")
	s.gh.branchHead = "def"
	r = s.run("apply")
	eq(t, "planfile apply skips the guard: exit code", 0, r.rc)
	lacks(t, "planfile apply skips the guard: no head lookup", r.api, "git/ref/heads")
}

func TestApplyWithoutPR(t *testing.T) {
	s := newScenario(t)
	s.gh.pr = ""
	r := s.run("apply")
	eq(t, "exit code", 0, r.rc)
	has(t, "skips the comment", r.out, "No PR associated with abc; skipping apply-result comment.")
}

// resolve-plan-run: merge commit -> its PR -> that PR's plan run.
func TestResolvePlanRun(t *testing.T) {
	s := newScenario(t).set("TFPR_PLAN_WORKFLOW", "pr.yaml")
	s.gh.planRun = "99"
	r := s.run("resolve-plan-run")
	eq(t, "exit code", 0, r.rc)
	has(t, "queries the head sha", r.api, "/repos/o/r/actions/workflows/pr.yaml/runs?head_sha=headsha&per_page=1&status=success")
	has(t, "says so", r.out, "Resolved PR #42 head=headsha → plan run=99")
	eq(t, "outputs", "run_id=99\npr_number=42\n", r.outputs)

	r = newScenario(t).set("TFPR_PLAN_WORKFLOW", "pr.yaml").run("resolve-plan-run")
	has(t, "no run: notice", r.out, "::notice::No successful 'pr.yaml' run found for head_sha=headsha (PR #42)")
	eq(t, "no run: outputs", "run_id=\npr_number=42\n", r.outputs)

	s = newScenario(t).set("TFPR_PLAN_WORKFLOW", "pr.yaml")
	s.gh.pr = ""
	r = s.run("resolve-plan-run")
	has(t, "no PR: notice", r.out, "::notice::No PR associated with abc")
	eq(t, "no PR: outputs", "run_id=\npr_number=\n", r.outputs)

	r = newScenario(t).set("TFPR_PLAN_WORKFLOW", "pr.yaml", "GITHUB_RUN_ATTEMPT", "2").run("resolve-plan-run")
	has(t, "re-run: notice", r.out, "::notice::Re-run detected (attempt=2)")
	eq(t, "re-run: no lookups", "", r.api)
}

// Outside a supported CI, outputs go to stdout and nothing calls the network.
func TestLocal(t *testing.T) {
	s := newScenario(t).set("GITHUB_ACTIONS", "")
	r := s.run("decide")
	eq(t, "decide: exit code", 0, r.rc)
	has(t, "decide: outputs on stdout", r.out, "download=false\nartifact_name=tfplan-stack-default\n")

	// No attempt counter: even a reviewed planfile is not trusted.
	s = newScenario(t).set("GITHUB_ACTIONS", "", "TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.planArtifact("stack", "default")
	r = s.run("decide")
	has(t, "decide: no download", r.out, "download=false")
	s.planArtifact("stack", "default")
	r = s.run("apply")
	eq(t, "planfile apply refused", 1, r.rc)
	lacks(t, "planfile apply refused: no apply", r.tf, "terraform apply")

	r = newScenario(t).set("GITHUB_ACTIONS", "").run("apply")
	eq(t, "fresh apply refused", 1, r.rc)
	lacks(t, "no apply", r.tf, "terraform apply")
	eq(t, "no API calls", "", r.api)

	r = newScenario(t).set("GITHUB_ACTIONS", "", "TFPR_PLAN_WORKFLOW", "pr.yaml").run("resolve-plan-run")
	eq(t, "resolve-plan-run needs GitHub", 1, r.rc)
}

// The API token the actions pass never reaches terraform; a GITHUB_TOKEN the
// caller sets for the GitHub provider does.
func TestTokenIsolation(t *testing.T) {
	s := newScenario(t).set("TFPR_GITHUB_TOKEN", "api-token", "GITHUB_TOKEN", "provider-token")
	r := s.run("apply")
	eq(t, "exit code", 0, r.rc)
	eq(t, "API auth", "Bearer api-token", s.gh.auth)
	env, err := os.ReadFile(s.env["FAKE_LOG"] + ".env")
	must(t, err)
	lacks(t, "terraform env", string(env), "TFPR_")
	has(t, "terraform env", string(env), "GITHUB_TOKEN=provider-token")

	s = newScenario(t).set("GH_TOKEN", "gh-token")
	s.run("apply")
	eq(t, "GH_TOKEN fallback", "Bearer gh-token", s.gh.auth)
}

// plan and comment can run as separate steps: comment needs only the plan dir
// and the API token, not terraform.
func TestPlanThenCommentInAnotherStep(t *testing.T) {
	s := newScenario(t).set("FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "changes"), "FAKE_TF_PLAN_CODE", "2")
	eq(t, "plan: exit code", 0, s.run("plan").rc)
	s.set("GITHUB_EVENT_NAME", "pull_request", "TFPR_GITHUB_TOKEN", "vcs-only", "TFPR_TF_BIN", "/nonexistent/terraform")
	r := s.run("comment")
	eq(t, "comment: exit code", 0, r.rc)
	eq(t, "comment: no terraform", "", r.tf)
	has(t, "comment: record", r.body, `"apply":["."]`)
	has(t, "comment: planset", r.body, `"planset":"sha256:`)
}

// TFPR_FILTER covers terraform's echoed output, the stored plan text, the
// names in plan-result.json and the comment body.
func TestPlanFilter(t *testing.T) {
	s := newScenario(t).set("FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "changes"), "FAKE_TF_PLAN_CODE", "2",
		"TFPR_FILTER", `sed -e s/secret-key/REDACTED/g -e 's/"beta"/"REDACTED"/g'`)
	r := s.run("plan")
	eq(t, "exit code", 0, r.rc)
	has(t, "echoed output", r.out, "terraform_data.REDACTED will be created")
	lacks(t, "echoed output", r.out, "secret-key")
	txt, err := os.ReadFile(filepath.Join(s.ws, "stack", ".tfpr", "units", "plan.txt"))
	must(t, err)
	has(t, "plan text", string(txt), `terraform_data.item["REDACTED"] will be destroyed`)
	lacks(t, "plan text", string(txt), `"beta"`)
	res := s.planResult()
	has(t, "plan-result.json", res, `"address": "terraform_data.item[\"REDACTED\"]"`)
	lacks(t, "plan-result.json", res, `beta`)
	has(t, "plan-result.json", res, `"mode": "filter"`)

	s.set("GITHUB_EVENT_NAME", "pull_request")
	r = s.run("comment")
	eq(t, "comment: exit code", 0, r.rc)
	has(t, "comment body", r.body, `terraform_data.item["REDACTED"] will be destroyed`)
	lacks(t, "comment body", r.body, `"beta"`)
}

func TestPlanFilterFailureFailsThePlan(t *testing.T) {
	r := newScenario(t).set("FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "changes"), "TFPR_FILTER", "exit 3").run("plan")
	eq(t, "exit code", 1, r.rc)
	has(t, "says why", r.out, "redaction filter failed")
}

// Bring your own plan: the caller's text still passes through TFPR_FILTER,
// both when derive stores it and when comment posts it.
func TestBYOFilter(t *testing.T) {
	s := newScenario(t).set("TFPR_FILTER", `sed 's/"beta"/"REDACTED"/g'`)
	s.byo("changes")
	txt, err := os.ReadFile(filepath.Join(s.ws, "stack", ".tfpr", "units", "plan.txt"))
	must(t, err)
	lacks(t, "stored plan text", string(txt), `"beta"`)
	res := s.planResult()
	lacks(t, "plan-result.json", res, "beta")
	has(t, "plan-result.json", res, `"runner": "byo"`)
	has(t, "plan-result.json", res, `"mode": "filter"`)

	// Without TFPR_FILTER, nothing filtered the names from plan.json, so the
	// result does not claim it: mode none. The egress filter still covers
	// every name in the comment body.
	s = newScenario(t)
	s.byo("changes")
	has(t, "no filter: mode", s.planResult(), `"mode": "none"`)
	has(t, "no filter: names as plan.json had them", s.planResult(), `terraform_data.item[\"beta\"]`)
	s.set("GITHUB_EVENT_NAME", "pull_request", "TFPR_FILTER", `sed 's/"beta"/"REDACTED"/g'`)
	r := s.run("comment")
	eq(t, "comment: exit code", 0, r.rc)
	has(t, "address in the resource list", r.body, `-   terraform_data.item["REDACTED"]   delete`)
	lacks(t, "comment body", r.body, `"beta"`)
}

// A filter failure posts nothing and fails the command.
func TestCommentFilterFailurePostsNothing(t *testing.T) {
	s := newScenario(t)
	s.byo("changes")
	s.set("GITHUB_EVENT_NAME", "pull_request", "TFPR_FILTER", "cat >/dev/null; exit 1")
	r := s.run("comment")
	eq(t, "exit code", 1, r.rc)
	has(t, "says why", r.out, "::error::redaction filter failed")
	has(t, "says nothing was posted", r.out, "nothing was posted")
	lacks(t, "no write", r.api, "POST")
	lacks(t, "no write", r.api, "PATCH")
}

func TestApplyCommentFilterFailure(t *testing.T) {
	s := newScenario(t).set("TFPR_FILTER", `in=$(cat); case "$in" in *"Terraform Apply"*) exit 1;; esac; printf '%s\n' "$in"`)
	r := s.run("apply")
	eq(t, "exit code", 1, r.rc)
	has(t, "says why", r.out, "nothing was posted")
	lacks(t, "no write", r.api, "POST")
	has(t, "terraform's own result stands in the outputs", r.outputs, "apply_exit_code=0")
}

// streamFilter fails only on terraform's echoed output (which carries
// STREAM-MARK), never on plan text or comment bodies.
const streamFilter = `in=$(cat); case "$in" in *STREAM-MARK*) exit 1;; esac; printf '%s\n' "$in"`

// A filter failure on terraform's output fails the step, but every report
// of terraform's result stays exact.
func TestStreamFilterFailureKeepsTerraformsResult(t *testing.T) {
	r := newScenario(t).set("TFPR_FILTER", streamFilter, "FAKE_TF_SAY", "STREAM-MARK").run("apply")
	eq(t, "apply: step fails", 1, r.rc)
	has(t, "apply: terraform's code in the outputs", r.outputs, "apply_exit_code=0")
	has(t, "apply: comment shows the true result", r.body, "succeeded")
	has(t, "apply: filter failure reported", r.out, "::error::redaction filter failed")

	r = newScenario(t).set("TFPR_FILTER", streamFilter, "FAKE_TF_SAY", "STREAM-MARK", "FAKE_TF_APPLY_CODE", "3").run("apply")
	eq(t, "failed apply: terraform's code is the exit status", 3, r.rc)
	has(t, "failed apply: outputs", r.outputs, "apply_exit_code=3")
	has(t, "failed apply: comment", r.body, "failed (exit 3)")

	s := newScenario(t).set("TFPR_FILTER", streamFilter, "FAKE_TF_SAY", "STREAM-MARK",
		"FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "changes"), "FAKE_TF_PLAN_CODE", "2")
	r = s.run("plan")
	eq(t, "plan: step fails", 1, r.rc)
	eq(t, "plan: terraform's code in the outputs", "exit_code=2\n", r.outputs)
	res := s.planResult()
	has(t, "plan: recorded as failed", res, `"status": "error"`)
	has(t, "plan: terraform's code kept", res, `"exit_code": 2,`)
	has(t, "plan: reason", res, "redaction filter failed")
}

// derive parses plan.json strictly and fails closed.
func TestDeriveRejectsBrokenPlanJSON(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join(root, "test", "fixtures", "render", "changes", "plan.json"))
	must(t, err)
	for name, data := range map[string]string{
		"stderr after":  string(fixture) + "\nWarning: something on stderr\n",
		"stderr before": "Warning: something on stderr\n" + string(fixture),
		"truncated":     string(fixture[:len(fixture)/2]),
		"not a plan":    `{"hello": "world"}`,
	} {
		s := newScenario(t)
		s.write("plan.json", data)
		s.write("plan.txt", "plan text\n")
		r := s.set("TFPR_PLAN_JSON", "plan.json", "TFPR_PLAN_TXT", "plan.txt").run("derive")
		eq(t, name+": exit code", 1, r.rc)
		has(t, name+": names the input", r.out, "plan.json is not the output of `terraform show -json`")
		res := s.planResult()
		has(t, name+": records the failure", res, `"status": "error"`)
		has(t, name+": records why", res, "plan.json is not the output of `terraform show -json`")
		lacks(t, name+": no plan text", res, `"present": true`)
	}
	s := newScenario(t)
	s.write("plan.json", string(fixture)+"\ntrailing")
	s.write("plan.txt", "x")
	has(t, "hint", s.set("TFPR_PLAN_JSON", "plan.json", "TFPR_PLAN_TXT", "plan.txt").run("derive").out, "stderr merged into stdout?")
}

// Missing inputs are a failed plan, recorded with the reason.
func TestDeriveMissingInputs(t *testing.T) {
	s := newScenario(t)
	r := s.run("derive")
	eq(t, "exit code", 1, r.rc)
	has(t, "message", r.out, "::error::TFPR_PLAN_JSON is not set")
	has(t, "recorded", s.planResult(), `"error": "TFPR_PLAN_JSON is not set`)

	s = newScenario(t).set("TFPR_PLAN_JSON", "gone.json", "TFPR_PLAN_TXT", "gone.txt", "TFPR_EXIT_CODE", "2")
	r = s.run("derive")
	eq(t, "missing files: exit code", 1, r.rc)
	res := s.planResult()
	has(t, "missing files: reason", res, "TFPR_PLAN_JSON: gone.json does not exist (did the plan run?)")
	has(t, "missing files: status", res, `"status": "error"`)
	has(t, "missing files: summary exit", res, `"exit_code": 1`)
}

// A failed plan replaces the stack's last good comment instead of leaving
// it standing as if current.
func TestPlanFailureReplacesTheComment(t *testing.T) {
	s := newScenario(t).set("FAKE_TF_PLAN_CODE", "1")
	r := s.run("plan")
	eq(t, "plan: exit code", 1, r.rc)
	res := s.planResult()
	has(t, "plan: status", res, `"status": "error"`)
	has(t, "plan: reason", res, `"error": "terraform plan exited 1"`)
	has(t, "plan: unit exit code", res, `"exit_code": 1,`)

	s.gh.comments = `[{"id":6,"user":{"login":"github-actions[bot]"},"body":"<!-- tf-plan:stack :: default -->\nold good plan"}]`
	r = s.set("GITHUB_EVENT_NAME", "pull_request").run("comment")
	eq(t, "comment: exit code", 0, r.rc)
	has(t, "comment: replaces the good plan", r.api, "PATCH /repos/o/r/issues/comments/6")
	golden(t, "comments/plan-failed.md", r.body)
}

func TestBYOFailedPlan(t *testing.T) {
	s := newScenario(t).set("TFPR_EXIT_CODE", "1", "TFPR_PLAN_JSON", "missing.json")
	r := s.run("derive")
	eq(t, "derive: exit code", 1, r.rc)
	has(t, "derive: says so", r.out, "the plan failed (TFPR_EXIT_CODE=1)")
	has(t, "derive: unit exit code", s.planResult(), `"exit_code": 1,`)
	r = s.set("GITHUB_EVENT_NAME", "pull_request").run("comment")
	eq(t, "comment: exit code", 0, r.rc)
	has(t, "comment: failed body", r.body, "**Plan failed.** Any earlier plan for `stack` is out of date")
	has(t, "comment: reason", r.body, "Reason: `the plan failed (TFPR_EXIT_CODE=1)`")
}

// A mistyped stack stays a clear error on every run: tfpr never creates the
// stack directory, even to record the failure.
func TestMissingStackIsNeverCreated(t *testing.T) {
	s := newScenario(t).set("TFPR_STACK", "typo")
	for run := 1; run <= 2; run++ {
		r := s.run("plan")
		eq(t, fmt.Sprintf("run %d: exit code", run), 1, r.rc)
		has(t, fmt.Sprintf("run %d: error", run), r.out, "::error::stack directory 'typo' not found")
		_, err := os.Stat(filepath.Join(s.ws, "typo"))
		eq(t, fmt.Sprintf("run %d: no stack dir", run), true, os.IsNotExist(err))
	}
	// comment still says the plan failed, without writing anything.
	r := s.set("GITHUB_EVENT_NAME", "pull_request").run("comment")
	eq(t, "comment: exit code", 1, r.rc)
	has(t, "comment: failed body", r.body, "Any earlier plan for `typo` is out of date")
	_, err := os.Stat(filepath.Join(s.ws, "typo"))
	eq(t, "comment: no stack dir", true, os.IsNotExist(err))

	// An explicit TFPR_DIR outside the stack still records the failure.
	s = newScenario(t).set("TFPR_STACK", "typo", "TFPR_DIR", "out")
	eq(t, "TFPR_DIR: exit code", 1, s.run("plan").rc)
	data, err := os.ReadFile(filepath.Join(s.ws, "out", "plan-result.json"))
	must(t, err)
	has(t, "TFPR_DIR: recorded", string(data), "stack directory 'typo' not found")
}

// With no plan dir at all, comment records and posts the failure itself and
// exits non-zero.
func TestCommentWithoutPlanDir(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request")
	r := s.run("comment")
	eq(t, "exit code", 1, r.rc)
	has(t, "posts", r.api, "POST /repos/o/r/issues/42/comments")
	has(t, "failed body", r.body, "**Plan failed.** Any earlier plan for `stack` is out of date")
	has(t, "reason", r.body, "no plan-result.json in stack/.tfpr")
	has(t, "records it", s.planResult(), `"status": "error"`)
}

// CI command lines from a filter's side output or from plan text never land
// in a comment. Warnings and errors among them are re-emitted, escaped; the
// rest are dropped and counted.
func TestCICommandsNeverReachTheComment(t *testing.T) {
	s := newScenario(t)
	plan, err := os.ReadFile(filepath.Join(root, "test", "fixtures", "render", "changes", "plan.txt"))
	must(t, err)
	s.write("plan.txt", string(plan)+"  ##vso[task.setvariable variable=x]pwned\n::set-output name=a::b\n::error::boom 100%\n")
	s.set("TFPR_PLAN_JSON", filepath.Join(root, "test", "fixtures", "render", "changes", "plan.json"), "TFPR_PLAN_TXT", "plan.txt",
		"TFPR_FILTER", `cat; echo '##vso[task.logissue type=warning]masked 3 values'`)
	r := s.run("derive")
	eq(t, "derive: exit code", 0, r.rc)
	has(t, "re-emits the filter's warning", r.out, "::warning::masked 3 values\n")
	has(t, "re-emits the plan's error, escaped", r.out, "::error::boom 100%25\n")
	has(t, "counts the rest", r.out, "::warning::Dropped 2 CI command line(s)")
	txt, err := os.ReadFile(filepath.Join(s.ws, "stack", ".tfpr", "units", "plan.txt"))
	must(t, err)
	for _, cmd := range []string{"##vso[", "\n::"} {
		lacks(t, "stored plan text", string(txt), cmd)
	}
	r = s.set("GITHUB_EVENT_NAME", "pull_request").run("comment")
	eq(t, "comment: exit code", 0, r.rc)
	for _, cmd := range []string{"##vso[", "::set-output", "::error", "masked 3 values"} {
		lacks(t, "comment body", r.body, cmd)
	}
}

// Terraform output and rendered comments are echoed where GitHub interprets
// no workflow commands.
func TestEchoedContentIsInert(t *testing.T) {
	s := newScenario(t).set("FAKE_TF_PLAN", filepath.Join(root, "test", "fixtures", "render", "changes"), "FAKE_TF_PLAN_CODE", "2",
		"FAKE_TF_SAY", "::warning::from terraform")
	r := s.run("plan")
	eq(t, "plan: exit code", 0, r.rc)
	eq(t, "terraform's command line is inside a stopped region", true, insideStopped(r.out, "::warning::from terraform"))

}

// The filter sees only the human-readable body. A filter that rewrites the
// stack name leaves the marker and record intact, so the comment is updated,
// not duplicated.
func TestFilterNeverTouchesMarkerOrRecord(t *testing.T) {
	s := newScenario(t).set("TFPR_STACK", "prod/", "TFPR_FILTER", "sed s/prod/X/g")
	must(t, os.MkdirAll(filepath.Join(s.ws, "prod"), 0o755))
	s.byo("no-changes")
	s.gh.comments = `[{"id":6,"user":{"login":"github-actions[bot]"},"body":"<!-- tf-plan:prod :: default -->\nold"}]`
	r := s.set("GITHUB_EVENT_NAME", "pull_request").run("comment")
	eq(t, "exit code", 0, r.rc)
	has(t, "updates", r.api, "PATCH /repos/o/r/issues/comments/6")
	lacks(t, "does not duplicate", r.api, "POST")
	lines := strings.SplitN(r.body, "\n", 3)
	eq(t, "marker unfiltered", "<!-- tf-plan:prod :: default -->", lines[0])
	has(t, "record unfiltered", lines[1], `"key":"prod:default"`)
	has(t, "body filtered", lines[2], "`X`")
	lacks(t, "body filtered", lines[2], "prod")
}

// render's stdout is data: exactly the body, which has no command lines.
func TestRenderOutputIsTheBody(t *testing.T) {
	s := newScenario(t)
	s.write("plan.txt", "::warning::injected\n  # a.b will be created\n")
	fixture := filepath.Join(root, "test", "fixtures", "render", "changes", "plan.json")
	eq(t, "derive", 0, s.set("TFPR_PLAN_JSON", fixture, "TFPR_PLAN_TXT", "plan.txt").run("derive").rc)
	r := s.run("render")
	eq(t, "exit code", 0, r.rc)
	if !strings.HasPrefix(r.out, "<!-- tf-plan:stack :: default -->\n") || !strings.HasSuffix(r.out, "redaction: none\n") {
		t.Errorf("render output is not just the body:\n%s", r.out)
	}
	lacks(t, "no markers", r.out, "stop-commands")
	lacks(t, "no command lines", r.out, "\n::")
}

// insideStopped reports whether line appears in out while GitHub's workflow
// commands are stopped (between ::stop-commands::<token> and ::<token>::).
func insideStopped(out, line string) bool {
	token := ""
	for _, l := range strings.Split(out, "\n") {
		switch {
		case token == "" && strings.HasPrefix(l, "::stop-commands::"):
			token = strings.TrimPrefix(l, "::stop-commands::")
		case token != "" && l == "::"+token+"::":
			token = ""
		case l == line:
			return token != ""
		}
	}
	return false
}

func TestDeriveRequiresValidExitCode(t *testing.T) {
	s := newScenario(t).set("TFPR_EXIT_CODE", "3")
	r := s.run("derive")
	eq(t, "exit code", 1, r.rc)
	has(t, "message", r.out, "TFPR_EXIT_CODE must be 0, 1 or 2")
	has(t, "recorded", s.planResult(), `"status": "error"`)
}

func TestFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tfplan")
	must(t, os.WriteFile(path, []byte("abc"), 0o644))
	got, err := fileSHA256(path)
	eq(t, "sha256", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", got)
	must(t, err)
	_, err = fileSHA256(filepath.Join(t.TempDir(), "missing"))
	eq(t, "missing file", true, err != nil)
}

func TestUsage(t *testing.T) {
	var out bytes.Buffer
	eq(t, "exit code", 1, run(nil, &out, &out))
	has(t, "message", out.String(), "tfpr: usage: tfpr plan|derive|render|comment|decide|apply|resolve-plan-run")
}

// comment writes the full plan to the job summary first, through the
// filter, and links it from the comment.
func TestCommentWritesTheJobSummary(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_FILTER", `sed 's/"beta"/"REDACTED"/g'`,
		"TFPR_PLAN_TXT_URL", "https://github.example/artifacts/9")
	s.byo("moves-only")
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	has(t, "summary: full plan, noise kept", r.summary, "# (1 unchanged attribute hidden)")
	has(t, "summary: expanded", r.summary, "<details open><summary>Plan output for stack</summary>")
	has(t, "summary: filtered", r.summary, `terraform_data.entry["REDACTED"]`)
	lacks(t, "summary: filtered", r.summary, `"beta"`)
	lacks(t, "comment: noise removed", r.body, "unchanged attribute hidden")
	has(t, "comment: links the summary", r.body, "[full plan](https://github.example/o/r/actions/runs/1)")

	// Off: no summary, no link.
	s = newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_SUMMARY", "false")
	s.byo("moves-only")
	r = s.run("comment")
	eq(t, "off: summary", "", r.summary)
	lacks(t, "off: no link", r.body, "[full plan]")
}

// Outside a pull request there is no comment, but the summary is written,
// with no API calls.
func TestSummaryWithoutPR(t *testing.T) {
	s := newScenario(t)
	s.byo("changes")
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	has(t, "skips the comment", r.out, "Not a pull request build; skipping plan comment.")
	has(t, "summary", r.summary, "**Resources (4)**")
	eq(t, "no API calls", "", r.api)
}

// TFPR_COMMENT_BUDGET caps the whole comment: marker, record and body.
func TestCommentBudget(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_COMMENT_BUDGET", "3000")
	s.byo("truncated")
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	if n := utf8.RuneCountInString(r.body); n > 3000 {
		t.Errorf("comment is %d characters, over 3,000", n)
	}
	has(t, "banner", strings.Join(strings.SplitN(r.body, "\n", 6)[:5], "\n"), "> **Plan text truncated**")
	eq(t, "bad budget", 1, s.set("TFPR_COMMENT_BUDGET", "lots").run("comment").rc)
}

// A filter that lengthens the body past the budget gets the minimal layout,
// filtered too.
func TestFilterThatLengthensFallsBack(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_COMMENT_BUDGET", "6000")
	s.byo("truncated")
	r := s.set("TFPR_FILTER", `sed 's/item/itemitemitemitemitemitem/g'`).run("comment")
	eq(t, "exit code", 0, r.rc)
	if n := utf8.RuneCountInString(r.body); n > 6000 {
		t.Errorf("comment is %d characters, over 6,000", n)
	}
	has(t, "minimal layout", r.body, "> **Comment shortened**")
}

// TFPR_TEMPLATE replaces named blocks; the example in the docs overrides
// only the footer.
func TestTemplateOverride(t *testing.T) {
	s := newScenario(t)
	s.write("footer.tmpl", "{{define \"footer\"}}\nOwners: @infra-team · [Runbook](https://example.invalid/runbook)\n{{end}}\n")
	s.byo("changes")
	r := s.set("TFPR_TEMPLATE", "footer.tmpl").run("render")
	eq(t, "exit code", 0, r.rc)
	if !strings.HasSuffix(r.out, "</details>\n\nOwners: @infra-team · [Runbook](https://example.invalid/runbook)\n") {
		t.Errorf("footer not replaced:\n%s", r.out)
	}
	has(t, "rest unchanged", r.out, "**Resources (4)**")

	r = s.set("TFPR_TEMPLATE", "missing.tmpl").run("render")
	eq(t, "missing template: exit code", 1, r.rc)
	has(t, "missing template: says so", r.out, "TFPR_TEMPLATE: open missing.tmpl")
}

// TFPR_COMMENT_BUDGET can lower the comment's size, never raise it past the
// host's limit: GitHub rejects longer comments.
func TestCommentBudgetIsClampedToTheHost(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_COMMENT_BUDGET", "200000")
	s.byo("large")
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	if n := utf8.RuneCountInString(r.body); n > 65536 {
		t.Errorf("comment is %d characters, over GitHub's 65,536", n)
	}
}
