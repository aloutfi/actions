package ci

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/aloutfi/actions/redact"
	"github.com/aloutfi/actions/vcs"
)

// GitHub is GitHub Actions.
type GitHub struct {
	env Env
	out io.Writer
	api *vcs.Client

	mu      sync.Mutex
	stopped int    // open Inert writers
	token   string // the current stop-commands token

	jobOnce sync.Once
	jobURL  string
}

func newGitHub(env Env, stdout, _ io.Writer, api *vcs.Client) CI {
	return &GitHub{env: env, out: stdout, api: api}
}

// Context reads the run from the GITHUB_* environment. On pull_request
// builds GITHUB_SHA is the merge commit that was checked out; the PR head
// comes from the event payload.
func (g *GitHub) Context() Ctx {
	e := g.env
	pr, head := g.pr()
	attempt := 1
	if a := e("GITHUB_RUN_ATTEMPT"); a != "" {
		attempt, _ = strconv.Atoi(a)
	}
	return Ctx{
		CI:      "github",
		RunID:   e("GITHUB_RUN_ID"),
		RunURL:  g.runURL(),
		SHA:     e("GITHUB_SHA"),
		HeadSHA: head,
		Ref:     e("GITHUB_REF"),
		PR:      pr,
		Repo:    e("GITHUB_REPOSITORY"),
		VCS:     "github",
		VCSURL:  e("GITHUB_API_URL"),
		Actor:   e("GITHUB_ACTOR"),
		Attempt: attempt,
	}
}

func (g *GitHub) runURL() string {
	return g.env("GITHUB_SERVER_URL") + "/" + g.env("GITHUB_REPOSITORY") + "/actions/runs/" + g.env("GITHUB_RUN_ID")
}

// pr is the PR number and head commit for pull_request builds, read from
// the event payload.
func (g *GitHub) pr() (number, head string) {
	if g.env("GITHUB_EVENT_NAME") != "pull_request" {
		return "", ""
	}
	data, err := os.ReadFile(g.env("GITHUB_EVENT_PATH"))
	if err != nil {
		return "", ""
	}
	var event struct {
		PullRequest struct {
			Number json.Number `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(data, &event) != nil {
		return "", ""
	}
	return event.PullRequest.Number.String(), event.PullRequest.Head.SHA
}

// JobURL links to this job's logs. The Actions context has no job id but the
// API does: match on runner_name, which is unique while the job is in
// progress and also tells matrix cells apart. Any failure falls back to the
// run URL. The lookup runs once.
func (g *GitHub) JobURL() string {
	g.jobOnce.Do(func() { g.jobURL = g.findJobURL() })
	return g.jobURL
}

func (g *GitHub) findJobURL() string {
	runURL := g.runURL()
	runner := g.env("RUNNER_NAME")
	var id int64
	path := fmt.Sprintf("/repos/%s/actions/runs/%s/jobs?per_page=100", g.env("GITHUB_REPOSITORY"), url.PathEscape(g.env("GITHUB_RUN_ID")))
	type page struct {
		Jobs []struct {
			ID         int64   `json:"id"`
			RunnerName *string `json:"runner_name"`
		} `json:"jobs"`
	}
	_ = vcs.GetPages(g.api, path, func(p page) bool {
		for _, j := range p.Jobs {
			if j.RunnerName != nil && *j.RunnerName == runner {
				id = j.ID
				return false
			}
		}
		return true
	})
	if id == 0 {
		return runURL
	}
	return runURL + "/job/" + strconv.FormatInt(id, 10)
}

// UploadSummary appends markdown to $GITHUB_STEP_SUMMARY, which GitHub
// shows on the run page (1 MiB per step).
func (g *GitHub) UploadSummary(markdown redact.Text) error {
	path := g.env("GITHUB_STEP_SUMMARY")
	if path == "" {
		return errors.New("GITHUB_STEP_SUMMARY is not set")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, markdown.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SummaryURL links to this job's summary on the run page
// (#summary-<job id>), or to the run page when the job id is unknown.
func (g *GitHub) SummaryURL() string {
	runURL := g.runURL()
	if id, ok := strings.CutPrefix(g.JobURL(), runURL+"/job/"); ok {
		return runURL + "#summary-" + id
	}
	return runURL
}

// RunFor links to this workflow's run for sha (the Actions runs API,
// filtered by head_sha), else to the commit.
func (g *GitHub) RunFor(sha string) string {
	repo := g.env("GITHUB_REPOSITORY")
	// GITHUB_WORKFLOW_REF is owner/repo/<path>@<ref>; runs carry <path>.
	workflow, _, _ := strings.Cut(strings.TrimPrefix(g.env("GITHUB_WORKFLOW_REF"), repo+"/"), "@")
	var runs struct {
		WorkflowRuns []struct {
			HTMLURL string `json:"html_url"`
			Path    string `json:"path"`
			HeadSHA string `json:"head_sha"`
		} `json:"workflow_runs"`
	}
	q := url.Values{"head_sha": {sha}, "per_page": {"30"}}
	if _, err := g.api.Do(http.MethodGet, "/repos/"+repo+"/actions/runs?"+q.Encode(), nil, &runs); err == nil {
		for _, r := range runs.WorkflowRuns {
			if r.HeadSHA == sha && (workflow == "" || r.Path == workflow) && r.HTMLURL != "" {
				return r.HTMLURL
			}
		}
	}
	return g.env("GITHUB_SERVER_URL") + "/" + repo + "/commit/" + sha
}

// SetOutput appends name=value to $GITHUB_OUTPUT.
func (g *GitHub) SetOutput(name, value string) error {
	return appendOutput(g.env("GITHUB_OUTPUT"), name, value)
}

// Log prints a workflow command (::warning:: and so on) on stdout, with the
// message escaped as workflow-command data.
func (g *GitHub) Log(level, msg string) {
	fmt.Fprintf(g.out, "::%s::%s\n", level, escapeData(msg))
}

var dataEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

func escapeData(s string) string { return dataEscaper.Replace(s) }

// Inert stops workflow-command processing (::stop-commands::<random token>)
// until every open Inert writer is closed (::<token>::). The markers go to
// stdout, where the runner reads commands; stop-commands applies to the whole
// step, so nested writers share one token.
func (g *GitHub) Inert(w io.Writer) io.WriteCloser {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped == 0 {
		var b [16]byte
		_, _ = rand.Read(b[:])
		g.token = hex.EncodeToString(b[:])
		fmt.Fprintf(g.out, "::stop-commands::%s\n", g.token)
	}
	g.stopped++
	return &inert{g: g, w: w, nl: true}
}

type inert struct {
	g      *GitHub
	w      io.Writer
	nl     bool // the last byte written was a newline (or nothing was written)
	closed bool
}

func (i *inert) Write(p []byte) (int, error) {
	if len(p) > 0 {
		i.nl = p[len(p)-1] == '\n'
	}
	return i.w.Write(p)
}

// Close ends the line it left open, so the resume marker starts a line of
// its own, then resumes commands once no writer is open.
func (i *inert) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	if !i.nl {
		_, _ = io.WriteString(i.w, "\n")
	}
	g := i.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped--; g.stopped == 0 {
		fmt.Fprintf(g.out, "::%s::\n", g.token)
	}
	return nil
}
