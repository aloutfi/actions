package ci

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloutfi/actions/redact"
	"github.com/aloutfi/actions/vcs"
)

func envOf(m map[string]string) Env { return func(k string) string { return m[k] } }

func TestDetect(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"GITHUB_ACTIONS": "true"}, "*ci.GitHub"},
		{map[string]string{"GITHUB_ACTIONS": "false"}, "*ci.Local"},
		{map[string]string{}, "*ci.Local"},
	} {
		got := Detect(envOf(c.env), io.Discard, io.Discard, nil)
		if name := typeName(got); name != c.want {
			t.Errorf("%v: got %s, want %s", c.env, name, c.want)
		}
	}
}

func typeName(c CI) string {
	switch c.(type) {
	case *GitHub:
		return "*ci.GitHub"
	case *Local:
		return "*ci.Local"
	}
	return "?"
}

func TestGitHubContext(t *testing.T) {
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"number":42}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"GITHUB_RUN_ID": "9", "GITHUB_SERVER_URL": "https://gh.example", "GITHUB_REPOSITORY": "o/r",
		"GITHUB_SHA": "abc", "GITHUB_REF": "refs/heads/main", "GITHUB_ACTOR": "me",
		"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": event,
	}
	got := (&GitHub{env: envOf(env)}).Context()
	want := Ctx{CI: "github", RunID: "9", RunURL: "https://gh.example/o/r/actions/runs/9", SHA: "abc",
		Ref: "refs/heads/main", PR: "42", Repo: "o/r", Actor: "me", Attempt: 1}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}

	env["GITHUB_EVENT_NAME"], env["GITHUB_RUN_ATTEMPT"] = "push", "3"
	got = (&GitHub{env: envOf(env)}).Context()
	if got.PR != "" || got.Attempt != 3 {
		t.Errorf("push build: PR %q, attempt %d", got.PR, got.Attempt)
	}
}

func TestGitHubJobURL(t *testing.T) {
	jobs := `{"jobs":[{"id":7,"runner_name":null},{"id":8,"runner_name":"r"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs/9/jobs" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, jobs)
	}))
	defer srv.Close()
	env := map[string]string{"GITHUB_RUN_ID": "9", "GITHUB_SERVER_URL": "https://gh.example",
		"GITHUB_REPOSITORY": "o/r", "RUNNER_NAME": "r"}
	g := &GitHub{env: envOf(env), api: vcs.NewGitHubClient(srv.URL, "")}
	if got := g.JobURL(); got != "https://gh.example/o/r/actions/runs/9/job/8" {
		t.Errorf("got %s", got)
	}
	if got := g.SummaryURL(); got != "https://gh.example/o/r/actions/runs/9#summary-8" {
		t.Errorf("summary: got %s", got)
	}
	env["RUNNER_NAME"] = ""
	if got := g.JobURL(); got != "https://gh.example/o/r/actions/runs/9/job/8" {
		t.Errorf("the job URL is looked up once: got %s", got)
	}
	g = &GitHub{env: envOf(env), api: vcs.NewGitHubClient(srv.URL, "")}
	if got := g.JobURL(); got != "https://gh.example/o/r/actions/runs/9" {
		t.Errorf("no matching runner: got %s", got)
	}
	if got := g.SummaryURL(); got != "https://gh.example/o/r/actions/runs/9" {
		t.Errorf("summary, no job id: got %s", got)
	}
}

// The job summary only takes filtered text, and appends: other steps may
// have written to the same file.
func TestUploadSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(path, []byte("earlier\n"), 0o644))
	text, err := redact.Configured(io.Discard, nil).Apply("### plan\n")
	must(err)
	must(newGitHub(envOf(map[string]string{"GITHUB_STEP_SUMMARY": path}), io.Discard, io.Discard, nil).UploadSummary(text))
	data, _ := os.ReadFile(path)
	if string(data) != "earlier\n### plan\n" {
		t.Errorf("github: %q", data)
	}
	if newGitHub(envOf(nil), io.Discard, io.Discard, nil).UploadSummary(text) == nil {
		t.Errorf("github without GITHUB_STEP_SUMMARY: no error")
	}

	local := filepath.Join(t.TempDir(), "tfpr-summary.md")
	must(newLocal(envOf(map[string]string{"TFPR_SUMMARY_FILE": local}), io.Discard, io.Discard, nil).UploadSummary(text))
	data, _ = os.ReadFile(local)
	if string(data) != "### plan\n" {
		t.Errorf("local: %q", data)
	}
	must(newLocal(envOf(nil), io.Discard, io.Discard, nil).UploadSummary(text))
}

func TestGitHubOutputsAndLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	var stdout bytes.Buffer
	g := newGitHub(envOf(map[string]string{"GITHUB_OUTPUT": path}), &stdout, io.Discard, nil)
	for _, kv := range [][2]string{{"a", "1"}, {"summary", "1 to add, 0 to change"}} {
		if err := g.SetOutput(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	g.Log(Warning, "careful")
	data, _ := os.ReadFile(path)
	if string(data) != "a=1\nsummary=1 to add, 0 to change\n" {
		t.Errorf("outputs: %q", data)
	}
	if stdout.String() != "::warning::careful\n" {
		t.Errorf("log: %q", stdout.String())
	}
}

func TestLocal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	l := newLocal(envOf(nil), &stdout, &stderr, nil)
	if (l.Context() != Ctx{CI: "local"}) || l.JobURL() != "" {
		t.Errorf("context: %+v", l.Context())
	}
	_ = l.SetOutput("a", "1")
	l.Log(Error, "bad")
	if stdout.String() != "a=1\n" || stderr.String() != "error: bad\n" {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}

	path := filepath.Join(t.TempDir(), "out")
	l = newLocal(envOf(map[string]string{"TFPR_OUTPUT_FILE": path}), &stdout, &stderr, nil)
	_ = l.SetOutput("b", "2")
	if data, _ := os.ReadFile(path); string(data) != "b=2\n" {
		t.Errorf("TFPR_OUTPUT_FILE: %q", data)
	}
}

func TestGitHubLogEscapes(t *testing.T) {
	var out bytes.Buffer
	g := newGitHub(envOf(nil), &out, io.Discard, nil)
	g.Log(Error, "100% done\r\n::set-output name=x::y")
	if out.String() != "::error::100%25 done%0D%0A::set-output name=x::y\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestGitHubInert(t *testing.T) {
	var out bytes.Buffer
	g := newGitHub(envOf(nil), &out, io.Discard, nil)
	a := g.Inert(&out)
	b := g.Inert(&out) // nested: one token for both
	io.WriteString(a, "::warning::from terraform\n")
	io.WriteString(b, "no newline at the end")
	b.Close()
	a.Close()
	a.Close() // idempotent
	lines := strings.Split(out.String(), "\n")
	token, ok := strings.CutPrefix(lines[0], "::stop-commands::")
	if !ok || len(token) != 32 {
		t.Fatalf("no stop marker: %q", out.String())
	}
	want := "::stop-commands::" + token + "\n::warning::from terraform\nno newline at the end\n::" + token + "::\n"
	if out.String() != want {
		t.Errorf("got %q\nwant %q", out.String(), want)
	}

	// A new region gets a new token.
	out.Reset()
	g.Inert(&out).Close()
	if strings.Contains(out.String(), token) {
		t.Errorf("token reused: %q", out.String())
	}
}

func TestLocalInertPassesThrough(t *testing.T) {
	var out bytes.Buffer
	w := newLocal(envOf(nil), io.Discard, io.Discard, nil).Inert(&out)
	io.WriteString(w, "::x::")
	w.Close()
	if out.String() != "::x::" {
		t.Errorf("got %q", out.String())
	}
}
