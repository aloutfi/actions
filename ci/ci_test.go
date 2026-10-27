package ci

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
	want := Ctx{RunID: "9", RunURL: "https://gh.example/o/r/actions/runs/9", SHA: "abc",
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
	env["RUNNER_NAME"] = ""
	if got := g.JobURL(); got != "https://gh.example/o/r/actions/runs/9" {
		t.Errorf("no matching runner: got %s", got)
	}
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
	if (l.Context() != Ctx{}) || l.JobURL() != "" {
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
