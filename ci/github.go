package ci

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/aloutfi/actions/vcs"
)

// GitHub is GitHub Actions.
type GitHub struct {
	env Env
	out io.Writer
	api *vcs.Client
}

func newGitHub(env Env, stdout, _ io.Writer, api *vcs.Client) CI {
	return &GitHub{env: env, out: stdout, api: api}
}

// Context reads the run from the GITHUB_* environment.
func (g *GitHub) Context() Ctx {
	e := g.env
	attempt := 1
	if a := e("GITHUB_RUN_ATTEMPT"); a != "" {
		attempt, _ = strconv.Atoi(a)
	}
	return Ctx{
		RunID:   e("GITHUB_RUN_ID"),
		RunURL:  g.runURL(),
		SHA:     e("GITHUB_SHA"),
		Ref:     e("GITHUB_REF"),
		PR:      g.pr(),
		Repo:    e("GITHUB_REPOSITORY"),
		Actor:   e("GITHUB_ACTOR"),
		Attempt: attempt,
	}
}

func (g *GitHub) runURL() string {
	return g.env("GITHUB_SERVER_URL") + "/" + g.env("GITHUB_REPOSITORY") + "/actions/runs/" + g.env("GITHUB_RUN_ID")
}

// pr is the PR number for pull_request builds, read from the event payload.
func (g *GitHub) pr() string {
	if g.env("GITHUB_EVENT_NAME") != "pull_request" {
		return ""
	}
	data, err := os.ReadFile(g.env("GITHUB_EVENT_PATH"))
	if err != nil {
		return ""
	}
	var event struct {
		PullRequest struct {
			Number json.Number `json:"number"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(data, &event) != nil {
		return ""
	}
	return event.PullRequest.Number.String()
}

// JobURL links to this job's logs. The Actions context has no job id but the
// API does: match on runner_name, which is unique while the job is in
// progress and also tells matrix cells apart. Any failure falls back to the
// run URL.
func (g *GitHub) JobURL() string {
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

// SetOutput appends name=value to $GITHUB_OUTPUT.
func (g *GitHub) SetOutput(name, value string) error {
	return appendOutput(g.env("GITHUB_OUTPUT"), name, value)
}

// Log prints a workflow command (::warning:: and so on) on stdout.
func (g *GitHub) Log(level, msg string) {
	fmt.Fprintf(g.out, "::%s::%s\n", level, msg)
}

// FindPlanRun is the id of the newest successful run of workflow (a file
// name) for headSHA in repo, or "". It queries GitHub Actions, so it lives
// here rather than on the repo host; resolve-plan-run is its only caller.
func (g *GitHub) FindPlanRun(repo, workflow, headSHA string) (string, error) {
	var runs struct {
		WorkflowRuns []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	q := url.Values{"head_sha": {headSHA}, "status": {"success"}, "per_page": {"1"}}
	path := fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?%s", repo, url.PathEscape(workflow), q.Encode())
	if _, err := g.api.Do(http.MethodGet, path, nil, &runs); err != nil {
		return "", err
	}
	if len(runs.WorkflowRuns) == 0 {
		return "", nil
	}
	return strconv.FormatInt(runs.WorkflowRuns[0].ID, 10), nil
}
