package vcs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aloutfi/actions/redact"
)

// server answers each request with the next of statuses (200 once they run
// out) and records the requests.
type server struct {
	mu       sync.Mutex
	statuses []int
	handle   func(w http.ResponseWriter, r *http.Request)
	calls    []string
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	s.calls = append(s.calls, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(body)))
	if len(s.statuses) > 0 {
		code := s.statuses[0]
		s.statuses = s.statuses[1:]
		if code != http.StatusOK {
			w.WriteHeader(code)
			io.WriteString(w, `{"message":"nope"}`)
			return
		}
	}
	s.handle(w, r)
}

func newServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request), statuses ...int) (*server, *Client) {
	t.Helper()
	retryDelay = 0
	s := &server{statuses: statuses, handle: handle}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, NewGitHubClient(srv.URL, "tok")
}

// text is s through the identity filter: the only way to make a redact.Text.
func text(t *testing.T, s string) redact.Text {
	t.Setenv("TFPR_FILTER", "")
	out, err := redact.Configured(io.Discard, nil).Apply(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ok(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }
}

func TestDoRetriesOnceOn5xxAnd429(t *testing.T) {
	for _, c := range []struct {
		name     string
		statuses []int
		calls    int
		status   int // of the returned error; 0 for success
	}{
		{"success", nil, 1, 0},
		{"502 then success", []int{502}, 2, 0},
		{"429 then success", []int{429}, 2, 0},
		{"two 503s", []int{503, 503}, 2, 503},
		{"404 is not retried", []int{404}, 1, 404},
	} {
		s, api := newServer(t, ok(`{"x":1}`), c.statuses...)
		var out struct{ X int }
		_, err := api.Do(http.MethodGet, "/thing", nil, &out)
		var se *StatusError
		switch {
		case c.status == 0 && (err != nil || out.X != 1):
			t.Errorf("%s: got %v, %+v", c.name, err, out)
		case c.status != 0 && (!errors.As(err, &se) || se.Status != c.status || se.Message != "nope"):
			t.Errorf("%s: want HTTP %d, got %v", c.name, c.status, err)
		}
		if len(s.calls) != c.calls {
			t.Errorf("%s: want %d calls, got %d", c.name, c.calls, len(s.calls))
		}
	}
}

func TestDoRetriesOnlyIdempotentMethods(t *testing.T) {
	for _, c := range []struct {
		method string
		calls  int
	}{
		{http.MethodGet, 2},
		{http.MethodPatch, 2},
		{http.MethodPost, 1},
	} {
		s, api := newServer(t, ok(`{}`), 502)
		_, err := api.Do(c.method, "/thing", map[string]string{"body": "b"}, nil)
		if len(s.calls) != c.calls {
			t.Errorf("%s: want %d calls, got %d", c.method, c.calls, len(s.calls))
		}
		if c.method == http.MethodPost && err == nil {
			t.Errorf("POST: want the 502 returned")
		}
	}
}

func TestClientSendsAuth(t *testing.T) {
	_, api := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization: %q", got)
		}
		io.WriteString(w, `{}`)
	})
	if _, err := api.Do(http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertComment(t *testing.T) {
	// Two pages; our comment is on the second, a look-alike by someone else on the first.
	var base string
	s, api := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method != http.MethodGet:
			io.WriteString(w, `{"id":1}`)
		case r.URL.Query().Get("page") == "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/42/comments?per_page=100&page=2>; rel="next"`, base))
			io.WriteString(w, `[{"id":5,"user":{"login":"someone"},"body":"<!-- m -->"}]`)
		default:
			io.WriteString(w, `[{"id":6,"user":{"login":"github-actions[bot]"},"body":"<!-- m -->\nold"}]`)
		}
	})
	base = api.BaseURL
	msg, err := NewGitHub(api, "o/r", "").UpsertComment(Comment{PR: "42", Marker: "<!-- m -->", Body: text(t, "new")})
	if err != nil || msg != "Updated comment 6 on PR #42" {
		t.Fatalf("got %q, %v", msg, err)
	}
	want, _ := json.Marshal(map[string]string{"body": "<!-- m -->\nnew"})
	if last := s.calls[len(s.calls)-1]; last != "PATCH /repos/o/r/issues/comments/6 "+string(want) {
		t.Errorf("last call: %q", last)
	}

	// Another author: nothing of ours exists, so post.
	s, api = newServer(t, ok(`[{"id":6,"user":{"login":"github-actions[bot]"},"body":"<!-- m -->"}]`))
	msg, err = NewGitHub(api, "o/r", "my-app[bot]").UpsertComment(Comment{PR: "42", Marker: "<!-- m -->", Body: text(t, "b")})
	if err != nil || msg != "Posted comment on PR #42" {
		t.Fatalf("got %q, %v", msg, err)
	}
	want, _ = json.Marshal(map[string]string{"body": "<!-- m -->\nb"})
	if last := s.calls[len(s.calls)-1]; last != "POST /repos/o/r/issues/42/comments "+string(want) {
		t.Errorf("last call: %q", last)
	}

	// A listing failure is returned, and nothing is posted.
	s, api = newServer(t, ok(`[]`), 404)
	if _, err := NewGitHub(api, "o/r", "").UpsertComment(Comment{PR: "42", Marker: "m", Body: text(t, "b")}); err == nil {
		t.Error("want an error when the listing fails")
	}
	for _, c := range s.calls {
		if strings.HasPrefix(c, "POST") {
			t.Errorf("posted after a failed listing: %s", c)
		}
	}

	// A write failure is returned.
	_, api = newServer(t, ok(`[]`), 200, 403)
	if _, err := NewGitHub(api, "o/r", "").UpsertComment(Comment{PR: "42", Marker: "m", Body: text(t, "b")}); err == nil {
		t.Error("want an error when the post fails")
	}
}

func TestPRsForCommitAndBranchHead(t *testing.T) {
	s, api := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			io.WriteString(w, `[{"number":1,"head":{"sha":"h1"}},{"number":2,"head":{"sha":"h2"}}]`)
			return
		}
		io.WriteString(w, `{"object":{"sha":"tip"}}`)
	})
	g := NewGitHub(api, "o/r", "")
	prs, err := g.PRsForCommit("abc")
	if err != nil || len(prs) != 2 || prs[1] != (PR{2, "h2"}) {
		t.Errorf("PRsForCommit: %+v, %v", prs, err)
	}
	head, err := g.BranchHead("feature/x y")
	if err != nil || head != "tip" {
		t.Errorf("BranchHead: %q, %v", head, err)
	}
	if got := s.calls[1]; got != "GET /repos/o/r/git/ref/heads/feature/x%20y" {
		t.Errorf("BranchHead call: %s", got)
	}
}

func TestNew(t *testing.T) {
	for _, c := range []struct {
		name, repo string
		want       string
	}{
		{"", "o/r", "*vcs.GitHub"},
		{"", "", "vcs.NoPR"},
		{"github", "o/r", "*vcs.GitHub"},
		{"none", "o/r", "vcs.NoPR"},
		{"gitlab", "o/r", "error"},
	} {
		v, err := New(Config{Name: c.name, Repo: c.repo, GitHub: NewGitHubClient(GitHubAPI, "")})
		got := fmt.Sprintf("%T", v)
		if err != nil {
			got = "error"
		}
		if got != c.want {
			t.Errorf("New(%q, %q): got %s, want %s", c.name, c.repo, got, c.want)
		}
	}
	if _, err := New(Config{Name: "gitlab"}); err == nil || err.Error() != "unknown TFPR_VCS 'gitlab'" {
		t.Errorf("unknown adapter error: %v", err)
	}
}

func TestNoPR(t *testing.T) {
	var v VCS = NoPR{}
	if prs, err := v.PRsForCommit("abc"); err != nil || len(prs) != 0 {
		t.Errorf("PRsForCommit: %v, %v", prs, err)
	}
	if _, err := v.BranchHead("main"); err == nil {
		t.Error("BranchHead must fail so the guard refuses")
	}
}

// The marker and record are tfpr's own lines, above the filtered body.
func TestCompose(t *testing.T) {
	if got := Compose("<!-- m -->", "<!-- r -->", text(t, "body\n")); got != "<!-- m -->\n<!-- r -->\nbody\n" {
		t.Errorf("got %q", got)
	}
	if got := Compose("<!-- m -->", "", text(t, "body\n")); got != "<!-- m -->\nbody\n" {
		t.Errorf("no record: got %q", got)
	}
}
