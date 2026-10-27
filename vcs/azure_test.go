package vcs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeADO is the slice of the Azure Repos REST API the adapter uses, for
// repository "proj/repo", with threads kept in memory.
type fakeADO struct {
	t          *testing.T
	me         string
	threads    []map[string]any
	failList   int // fail the next N thread listings with HTTP 500
	failCreate bool
	query      map[string][]map[string]any // pullrequestquery results by sha
	commits    map[string]string           // commit message by sha
	prs        map[string]map[string]any   // PR by id
	refs       []map[string]any

	mu    sync.Mutex
	calls []string
}

var (
	reThreads  = regexp.MustCompile(`^/proj/_apis/git/repositories/repo/pullRequests/(\d+)/threads$`)
	reThread   = regexp.MustCompile(`^/proj/_apis/git/repositories/repo/pullRequests/\d+/threads/(\d+)$`)
	reComments = regexp.MustCompile(`^/proj/_apis/git/repositories/repo/pullRequests/\d+/threads/(\d+)/comments(?:/(\d+))?$`)
)

func (f *fakeADO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	if strings.Contains(p, "/_apis/git/") && r.URL.Query().Get("api-version") != "7.1" {
		f.t.Errorf("%s %s: api-version %q", r.Method, r.URL, r.URL.Query().Get("api-version"))
	}
	switch {
	case p == "/_apis/connectionData":
		if f.me == "" {
			http.Error(w, `{"message":"nope"}`, http.StatusUnauthorized)
			return
		}
		reply(map[string]any{"authenticatedUser": map[string]any{"id": f.me}})
	case reThreads.MatchString(p) && r.Method == http.MethodGet:
		if f.failList > 0 {
			f.failList--
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		reply(map[string]any{"value": f.threads, "count": len(f.threads)})
	case reThreads.MatchString(p) && r.Method == http.MethodPost:
		if f.failCreate {
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		id := len(f.threads) + 100
		comments := in["comments"].([]any)
		c := comments[0].(map[string]any)
		c["id"], c["author"] = float64(1), map[string]any{"id": f.me}
		in["id"] = id
		f.threads = append(f.threads, in)
		reply(in)
	case reComments.MatchString(p):
		m := reComments.FindStringSubmatch(p)
		t := f.thread(m[1])
		comments := t["comments"].([]any)
		if r.Method == http.MethodPatch {
			for _, c := range comments {
				if strconv.Itoa(int(c.(map[string]any)["id"].(float64))) == m[2] {
					c.(map[string]any)["content"] = in["content"]
				}
			}
		} else {
			in["id"], in["author"] = float64(len(comments)+1), map[string]any{"id": f.me}
			t["comments"] = append(comments, in)
		}
		reply(in)
	case reThread.MatchString(p) && r.Method == http.MethodPatch:
		t := f.thread(reThread.FindStringSubmatch(p)[1])
		if s, ok := in["status"]; ok {
			t["status"] = s
		}
		props, _ := t["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		for k, v := range in["properties"].(map[string]any) {
			props[k] = v
		}
		t["properties"] = props
		reply(t)
	case strings.HasSuffix(p, "/pullrequestquery"):
		sha := in["queries"].([]any)[0].(map[string]any)["items"].([]any)[0].(string)
		reply(map[string]any{"results": []any{map[string]any{sha: f.query[sha]}}})
	case strings.Contains(p, "/commits/"):
		sha := p[strings.LastIndex(p, "/")+1:]
		reply(map[string]any{"commitId": sha, "comment": f.commits[sha]})
	case strings.Contains(p, "/pullrequests/"):
		reply(f.prs[p[strings.LastIndex(p, "/")+1:]])
	case strings.HasSuffix(p, "/refs"):
		reply(map[string]any{"value": f.refs})
	default:
		f.t.Errorf("fake ADO: unexpected %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (f *fakeADO) thread(id string) map[string]any {
	for _, t := range f.threads {
		if fmt.Sprint(t["id"]) == id {
			return t
		}
	}
	f.t.Fatalf("no thread %s", id)
	return nil
}

// existing is a thread as Azure Repos returns it.
func existing(id int, author, marker, fingerprint, status string) map[string]any {
	return map[string]any{
		"id": float64(id), "status": status,
		"properties": map[string]any{
			propMarker:      map[string]any{"$type": "System.String", "$value": marker},
			propFingerprint: map[string]any{"$type": "System.String", "$value": fingerprint},
		},
		"comments": []any{map[string]any{"id": float64(1), "author": map[string]any{"id": author}, "content": "old"}},
	}
}

func newADO(t *testing.T, f *fakeADO, ack bool) *Azure {
	t.Helper()
	retryDelay = 0
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return NewAzure(NewAzureClient(srv.URL+"/", "tok"), "proj/repo", ack)
}

const marker = "<!-- tf-plan:infra :: default -->"

func plan(t *testing.T, body, fp string, resolved bool) Comment {
	return Comment{PR: "7", Marker: marker, Record: `<!-- tfpr:plan@1 {"key":"infra:default"} -->`,
		Body: text(t, body), Fingerprint: fp, Resolved: resolved}
}

func TestAzureCreatesAThreadWithProperties(t *testing.T) {
	f := &fakeADO{me: "build-id", threads: []map[string]any{
		existing(5, "someone-else", marker, "fp1", "active"), // same marker, not ours
	}}
	msg, err := newADO(t, f, false).UpsertComment(plan(t, "the plan", "fp1", false))
	if err != nil || msg != "Posted thread on PR #7" {
		t.Fatalf("got %q, %v", msg, err)
	}
	created := f.threads[1]
	if created["status"] != "active" {
		t.Errorf("status %v", created["status"])
	}
	content := created["comments"].([]any)[0].(map[string]any)["content"]
	if content != "the plan" {
		t.Errorf("content %q: the marker and record belong in properties", content)
	}
	props := created["properties"].(map[string]any)
	for name, want := range map[string]string{propMarker: marker, propRecord: `<!-- tfpr:plan@1 {"key":"infra:default"} -->`, propFingerprint: "fp1"} {
		if v := props[name].(map[string]any); v["$value"] != want || v["$type"] != "System.String" {
			t.Errorf("property %s: %v", name, v)
		}
	}
	if f.threads[0]["comments"].([]any)[0].(map[string]any)["content"] != "old" {
		t.Error("touched another identity's thread")
	}
}

func TestAzureUpdatesItsThread(t *testing.T) {
	f := &fakeADO{me: "build-id", threads: []map[string]any{existing(5, "build-id", marker, "fp1", "fixed")}}
	z := newADO(t, f, false)
	msg, err := z.UpsertComment(plan(t, "new plan", "fp2", false))
	if err != nil || msg != "Updated thread 5 on PR #7" {
		t.Fatalf("got %q, %v", msg, err)
	}
	th := f.threads[0]
	if th["comments"].([]any)[0].(map[string]any)["content"] != "new plan" || th["status"] != "active" || len(f.threads) != 1 {
		t.Errorf("thread %v", th)
	}
	// A no-op plan closes the thread.
	if _, err := z.UpsertComment(plan(t, "no changes", "fp3", true)); err != nil {
		t.Fatal(err)
	}
	if th["status"] != "closed" {
		t.Errorf("no-op: status %v", th["status"])
	}
}

// With ack, an unchanged plan keeps the reviewer's resolution; a changed one
// reopens the thread with a reply.
func TestAzureAck(t *testing.T) {
	f := &fakeADO{me: "build-id", threads: []map[string]any{existing(5, "build-id", marker, "fp1", "fixed")}}
	z := newADO(t, f, true)
	if _, err := z.UpsertComment(plan(t, "same plan", "fp1", false)); err != nil {
		t.Fatal(err)
	}
	th := f.threads[0]
	if th["status"] != "fixed" || len(th["comments"].([]any)) != 1 {
		t.Errorf("unchanged plan: %v", th)
	}
	if _, err := z.UpsertComment(plan(t, "changed plan", "fp2", false)); err != nil {
		t.Fatal(err)
	}
	comments := th["comments"].([]any)
	if th["status"] != "active" || len(comments) != 2 || comments[1].(map[string]any)["content"] != ackReply {
		t.Errorf("changed plan: %v", th)
	}
	// Changed again while still active: no second reply.
	if _, err := z.UpsertComment(plan(t, "changed again", "fp3", false)); err != nil {
		t.Fatal(err)
	}
	if len(th["comments"].([]any)) != 2 {
		t.Error("replied again while the thread was already active")
	}
	// A new thread under ack starts active, a no-op one closed.
	f.threads = nil
	_, _ = z.UpsertComment(plan(t, "x", "fp", false))
	if f.threads[0]["status"] != "active" {
		t.Errorf("new thread: %v", f.threads[0]["status"])
	}
}

// Only a reviewer's resolution counts as acknowledged: not tfpr closing a
// no-op thread, and not "pending".
func TestAzureAckOnlyCountsReviewers(t *testing.T) {
	f := &fakeADO{me: "build-id"}
	z := newADO(t, f, true)
	replies := func() int { return len(f.threads[0]["comments"].([]any)) - 1 }

	if _, err := z.UpsertComment(plan(t, "no changes", "fp1", true)); err != nil {
		t.Fatal(err)
	}
	th := f.threads[0]
	if th["status"] != "closed" || th["properties"].(map[string]any)[propStatus].(map[string]any)["$value"] != "closed" {
		t.Fatalf("no-op thread: %v", th)
	}
	// The plan changes: tfpr closed the thread, nobody acknowledged it.
	_, _ = z.UpsertComment(plan(t, "changes", "fp2", false))
	if th["status"] != "active" || replies() != 0 {
		t.Errorf("after tfpr's own close: status %v, replies %d", th["status"], replies())
	}
	// A reviewer resolves it, then the plan changes: reopen and say why.
	th["status"] = "fixed"
	_, _ = z.UpsertComment(plan(t, "more changes", "fp3", false))
	if th["status"] != "active" || replies() != 1 {
		t.Errorf("after a reviewer's resolution: status %v, replies %d", th["status"], replies())
	}
	// "pending" is no resolution.
	th["status"] = "pending"
	_, _ = z.UpsertComment(plan(t, "yet more", "fp4", false))
	if th["status"] != "active" || replies() != 1 {
		t.Errorf("after pending: status %v, replies %d", th["status"], replies())
	}
	// A reviewer closes it (the same word tfpr uses): that is a resolution.
	th["status"] = "closed"
	_, _ = z.UpsertComment(plan(t, "and again", "fp5", false))
	if replies() != 2 {
		t.Errorf("after a reviewer's close: replies %d", replies())
	}
}

func TestAzureListingFailurePostsNothing(t *testing.T) {
	f := &fakeADO{me: "build-id", failList: 2}
	if _, err := newADO(t, f, false).UpsertComment(plan(t, "x", "fp", false)); err == nil || !strings.Contains(err.Error(), "list threads on PR #7") {
		t.Errorf("got %v", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "POST") {
			t.Errorf("posted after a failed listing: %v", f.calls)
		}
	}
	if len(f.calls) != 3 { // connectionData, then the listing and its one retry
		t.Errorf("calls %v", f.calls)
	}
}

func TestAzureIdentityFailurePostsNothing(t *testing.T) {
	f := &fakeADO{}
	if _, err := newADO(t, f, false).UpsertComment(plan(t, "x", "fp", false)); err == nil || !strings.Contains(err.Error(), "build identity") {
		t.Errorf("got %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls %v", f.calls)
	}
}

func TestAzureCreateIsNotRetried(t *testing.T) {
	f := &fakeADO{me: "build-id", failCreate: true}
	if _, err := newADO(t, f, false).UpsertComment(plan(t, "x", "fp", false)); err == nil {
		t.Error("want the 500")
	}
	posts := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "POST") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("POSTed %d times", posts)
	}
}

func TestAzurePRsForCommit(t *testing.T) {
	pr := func(id int, status, target, mergeCommit string) map[string]any {
		return map[string]any{"pullRequestId": id, "status": status, "targetRefName": target,
			"lastMergeSourceCommit": map[string]any{"commitId": fmt.Sprintf("head%d", id)},
			"lastMergeCommit":       map[string]any{"commitId": mergeCommit}}
	}
	f := &fakeADO{
		query: map[string][]map[string]any{
			"merge1": {{"pullRequestId": 7, "lastMergeSourceCommit": map[string]any{"commitId": "head7"}}},
		},
		commits: map[string]string{
			"squash1":   "Merged PR 9: tidy the network\n\nRelated work items: #1",
			"plain":     "fix typo",
			"forged":    "Merged PR 9: a title anyone can type", // PR 9 merged squash1, not this
			"elsewhere": "Merged PR 10: merged into another branch",
			"open":      "Merged PR 11: still open",
		},
		prs: map[string]map[string]any{
			"9":  pr(9, "completed", "refs/heads/main", "squash1"),
			"10": pr(10, "completed", "refs/heads/release", "elsewhere"),
			"11": pr(11, "active", "refs/heads/main", "open"),
		},
	}
	z := newADO(t, f, false)
	for sha, want := range map[string]string{
		"merge1": "[{7 head7}]", "squash1": "[{9 head9}]",
		"plain": "[]", "forged": "[]", "elsewhere": "[]", "open": "[]",
	} {
		prs, err := z.PRsForCommit(sha, "refs/heads/main")
		if got := fmt.Sprint(prs); err != nil || got != want {
			t.Errorf("%s: got %s, %v; want %s", sha, got, err, want)
		}
	}
}

func TestAzureBranchHead(t *testing.T) {
	f := &fakeADO{refs: []map[string]any{
		{"name": "refs/heads/main-old", "objectId": "x"},
		{"name": "refs/heads/main", "objectId": "tip"},
	}}
	z := newADO(t, f, false)
	if head, err := z.BranchHead("main"); err != nil || head != "tip" {
		t.Errorf("got %q, %v", head, err)
	}
	if _, err := z.BranchHead("missing"); err == nil {
		t.Error("want an error for a missing branch")
	}
}

func TestNewAzure(t *testing.T) {
	collection := NewAzureClient("https://dev.azure.example/org/", "")
	if _, err := New(Config{Name: "azure", Azure: collection, Repo: "proj/repo"}); err != nil {
		t.Error(err)
	}
	for _, c := range []Config{{Name: "azure", Repo: "proj/repo"}, {Name: "azure", Azure: NewAzureClient("", ""), Repo: "proj/repo"},
		{Name: "azure", Azure: collection, Repo: "repo"}} {
		if _, err := New(c); err == nil {
			t.Errorf("%+v: want an error", c)
		}
	}
}

func TestAzureRecords(t *testing.T) {
	withRecord := func(id int, author, rec string) map[string]any {
		th := existing(id, author, marker, "fp", "active")
		th["properties"].(map[string]any)[propRecord] = map[string]any{"$type": "System.String", "$value": rec}
		return th
	}
	f := &fakeADO{me: "build-id", threads: []map[string]any{
		withRecord(5, "someone-else", `<!-- tfpr:plan@1 {"status":"no_op"} -->`), // forged: not the build identity
		withRecord(6, "build-id", `<!-- tfpr:plan@1 {"status":"changes"} -->`),
	}}
	recs, err := newADO(t, f, false).Records("7", marker)
	if err != nil || len(recs) != 1 || recs[0] != `<!-- tfpr:plan@1 {"status":"changes"} -->` {
		t.Errorf("got %q, %v", recs, err)
	}
}
