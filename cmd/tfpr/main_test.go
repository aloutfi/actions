package main

import (
	"bytes"
	"encoding/json"
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
)

// fakeGitHub is the GitHub REST API as tfpr uses it. Its fields shape the
// responses; calls and the last written comment body are recorded.
type fakeGitHub struct {
	t          *testing.T
	pr         string // PR number associated with every commit; "" for none
	branchHead string
	planRun    string
	failWrites bool
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
  plan) exit "${FAKE_TF_PLAN_CODE:-0}" ;;
  show) if [ "$2" = "-json" ]; then cat "$FAKE_TF_PLAN/plan.json"; else cat "$FAKE_TF_PLAN/plan.txt"; fi ;;
  apply) exit "${FAKE_TF_APPLY_CODE:-0}" ;;
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
		"TFPR_STACK": "stack", "TFPR_WORKSPACE": "default",
	}
	// Anything the host CI sets must not leak in.
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "TFPR_GITHUB_TOKEN", "TFPR_EXIT_CODE", "TFPR_RUN_ID", "TFPR_JOB_URL",
		"TFPR_COMMENT_BUDGET", "TFPR_COMMENT_AUTHOR", "TFPR_USE_PLANFILE", "TFPR_ALLOW_FRESH_PLAN_FALLBACK",
		"TFPR_PLAN_DIR", "TFPR_PLAN_RUN_ID", "TFPR_LOCK_TIMEOUT", "TFPR_PLAN_WORKFLOW", "TFPR_VCS",
		"TFPR_REPO", "TFPR_OUTPUT_FILE", "FAKE_TF_PLAN", "FAKE_TF_PLAN_CODE", "FAKE_TF_APPLY_CODE"} {
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

func (s *scenario) copyFixture(fixture string) {
	for _, f := range []string{"plan.json", "plan.txt"} {
		data, err := os.ReadFile(filepath.Join(root, "test", "fixtures", "render", fixture, f))
		must(s.t, err)
		must(s.t, os.WriteFile(filepath.Join(s.ws, "stack", f), data, 0o644))
	}
}

func (s *scenario) run(cmd string) result {
	for k, v := range s.env {
		s.t.Setenv(k, v)
	}
	s.t.Chdir(s.ws)
	var out bytes.Buffer
	rc := run([]string{cmd}, &out, &out)
	read := func(name string) string {
		data, _ := os.ReadFile(s.env[name])
		return string(data)
	}
	return result{rc: rc, out: out.String(), outputs: read("GITHUB_OUTPUT"), tf: read("FAKE_LOG"),
		api: strings.Join(s.gh.calls, "\n"), body: s.gh.body}
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

// golden compares got with test/fixtures/<file>, shared with test/run.sh.
func golden(t *testing.T, file, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join(root, "test", "fixtures", file))
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
	txt, err := os.ReadFile(filepath.Join(s.ws, "stack", "plan.txt"))
	must(t, err)
	want, err := os.ReadFile(filepath.Join(root, "test", "fixtures", "render", "changes", "plan.txt"))
	must(t, err)
	eq(t, "plan.txt", string(want), string(txt))
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
	s.copyFixture("no-changes")
	s.gh.failWrites = true
	r = s.run("comment")
	eq(t, "plan comment: exit code", 0, r.rc)
	has(t, "plan comment: warns", r.out, "::warning::Could not post the plan comment on PR #42")
}

func TestCommentPosts(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_EXIT_CODE", "0")
	s.copyFixture("no-changes")
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	has(t, "posts", r.api, "POST /repos/o/r/issues/42/comments")
	has(t, "says so", r.out, "Posted comment on PR #42")
	golden(t, "comments/plan.md", r.body)
}

func TestCommentUpdatesOwnComment(t *testing.T) {
	s := newScenario(t).set("GITHUB_EVENT_NAME", "pull_request", "TFPR_EXIT_CODE", "0")
	s.copyFixture("no-changes")
	s.gh.comments = `[{"id":5,"user":{"login":"someone"},"body":"<!-- tf-plan:stack :: default -->"},
		{"id":6,"user":{"login":"github-actions[bot]"},"body":"<!-- tf-plan:stack :: default -->\nold"}]`
	r := s.run("comment")
	has(t, "patches its own", r.api, "PATCH /repos/o/r/issues/comments/6")
	has(t, "says so", r.out, "Updated comment 6 on PR #42")
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

func TestUsage(t *testing.T) {
	var out bytes.Buffer
	eq(t, "exit code", 1, run(nil, &out, &out))
	has(t, "message", out.String(), "tfpr: usage: tfpr plan|render|comment|decide|apply|resolve-plan-run")
}
