package vcs

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aloutfi/actions/redact"
)

// DefaultGitHubAuthor is the login of the default GITHUB_TOKEN identity.
const DefaultGitHubAuthor = "github-actions[bot]"

// GitHubAPI is the public GitHub API.
const GitHubAPI = "https://api.github.com"

// NewGitHubClient is a REST client for the GitHub API at apiURL,
// authenticated with token if set. There is no default URL: the caller
// decides, so an enterprise token is never sent to the public API.
func NewGitHubClient(apiURL, token string) *Client {
	h := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "tfpr",
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return &Client{BaseURL: apiURL, Header: h}
}

// GitHub is the GitHub repo host, over the REST API.
type GitHub struct {
	api    *Client
	repo   string // owner/repo
	author string
}

// NewGitHub returns the adapter for repo (owner/repo). Only comments by
// author are ever edited. With no author (outside GitHub Actions, where the
// default token's login is github-actions[bot]), the adapter asks GitHub
// whose token it holds (GET /user) before it posts anything.
func NewGitHub(api *Client, repo, author string) *GitHub {
	return &GitHub{api: api, repo: repo, author: author}
}

// ErrNoAuthor means tfpr can't tell which comments are its own, so it posts
// none: posting anyway would duplicate the comment on every run.
var ErrNoAuthor = errors.New("the comment author is unknown")

// login is the author whose comments the adapter may edit.
func (g *GitHub) login() (string, error) {
	if g.author != "" {
		return g.author, nil
	}
	var user struct {
		Login string `json:"login"`
	}
	if _, err := g.api.Do(http.MethodGet, "/user", nil, &user); err != nil || user.Login == "" {
		return "", fmt.Errorf("%w: GET /user failed (%v), as it does for GitHub App installation tokens. Set TFPR_COMMENT_AUTHOR to the login that posts, e.g. <app-slug>[bot]", ErrNoAuthor, err)
	}
	g.author = user.Login
	return g.author, nil
}

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// Compose is a comment as GitHub stores it: the marker on line 1, the record
// (if any) on line 2, then the filtered body.
func Compose(marker, record string, body redact.Text) string {
	head := marker + "\n"
	if record != "" {
		head += record + "\n"
	}
	return head + body.String()
}

// UpsertComment edits the first comment by the adapter's author that starts
// with the marker, or posts a new one. A human who pastes the marker into
// their own comment never has it overwritten. GitHub issue comments have no
// status, so Resolved and Fingerprint don't apply.
func (g *GitHub) UpsertComment(c Comment) (string, error) {
	pr, marker := c.PR, c.Marker
	author, err := g.login()
	if err != nil {
		return "", err
	}
	// If the listing fails, tfpr cannot know whether its comment exists, and
	// posting could duplicate it, so it gives up instead.
	own, err := g.own(pr, marker, author, true)
	if err != nil {
		return "", err
	}
	var id int64
	if len(own) > 0 {
		id = own[0].ID
	}
	payload := map[string]string{"body": Compose(marker, c.Record, c.Body)}
	if id != 0 {
		if _, err := g.api.Do(http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", g.repo, id), payload, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("Updated comment %d on PR #%s", id, pr), nil
	}
	if _, err := g.api.Do(http.MethodPost, fmt.Sprintf("/repos/%s/issues/%s/comments", g.repo, url.PathEscape(pr)), payload, nil); err != nil {
		return "", err
	}
	return "Posted comment on PR #" + pr, nil
}

// own lists author's comments on pr that start with marker; with first, it
// stops at the first one.
func (g *GitHub) own(pr, marker, author string, first bool) ([]issueComment, error) {
	var found []issueComment
	err := GetPages(g.api, fmt.Sprintf("/repos/%s/issues/%s/comments?per_page=100", g.repo, url.PathEscape(pr)),
		func(page []issueComment) bool {
			for _, ic := range page {
				if ic.User.Login == author && strings.HasPrefix(ic.Body, marker) {
					found = append(found, ic)
					if first {
						return false
					}
				}
			}
			return true
		})
	if err != nil {
		return nil, fmt.Errorf("list comments on PR #%s: %w", pr, err)
	}
	return found, nil
}

// Records reads the record line (the comment's second line) from every
// comment of the adapter's author for marker.
func (g *GitHub) Records(pr, marker string) ([]string, error) {
	author, err := g.login()
	if err != nil {
		return nil, err
	}
	own, err := g.own(pr, marker, author, false)
	if err != nil {
		return nil, err
	}
	records := make([]string, 0, len(own))
	for _, c := range own {
		lines := strings.SplitN(c.Body, "\n", 3)
		rec := ""
		if len(lines) > 1 && strings.HasPrefix(lines[1], "<!-- tfpr:plan@") {
			rec = lines[1]
		}
		records = append(records, rec)
	}
	return records, nil
}

// PRsForCommit lists the PRs associated with sha (first page only). GitHub
// answers this directly, so the ref is not needed.
func (g *GitHub) PRsForCommit(sha, _ string) ([]PR, error) {
	var pulls []struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if _, err := g.api.Do(http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/pulls", g.repo, url.PathEscape(sha)), nil, &pulls); err != nil {
		return nil, err
	}
	prs := make([]PR, 0, len(pulls))
	for _, p := range pulls {
		prs = append(prs, PR{Number: p.Number, HeadSHA: p.Head.SHA})
	}
	return prs, nil
}

// BranchHead is the commit SHA at the tip of branch.
func (g *GitHub) BranchHead(branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := g.api.Do(http.MethodGet, fmt.Sprintf("/repos/%s/git/ref/heads/%s", g.repo, escapePath(branch)), nil, &ref); err != nil {
		return "", err
	}
	if ref.Object.SHA == "" {
		return "", fmt.Errorf("no head commit for branch '%s'", branch)
	}
	return ref.Object.SHA, nil
}

// compareFileLimit is the most files GitHub's compare API lists.
const compareFileLimit = 300

// ChangedFiles lists the files changed from base to head with the compare
// API, which lists at most 300: more fails.
func (g *GitHub) ChangedFiles(base, head string) ([]string, error) {
	var cmp struct {
		Status string `json:"status"`
		Files  []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		} `json:"files"`
	}
	// per_page=1: only the first page lists files, and no commits are needed.
	p := fmt.Sprintf("/repos/%s/compare/%s...%s?per_page=1", g.repo, url.PathEscape(base), url.PathEscape(head))
	if _, err := g.api.Do(http.MethodGet, p, nil, &cmp); err != nil {
		return nil, err
	}
	if cmp.Status != "ahead" {
		return nil, fmt.Errorf("%s is not an ancestor of %s (compare status '%s')", base, head, cmp.Status)
	}
	if len(cmp.Files) >= compareFileLimit {
		return nil, fmt.Errorf("%d or more files changed from %s to %s; GitHub lists only %d", compareFileLimit, base, head, compareFileLimit)
	}
	var files []string
	for _, f := range cmp.Files {
		files = append(files, f.Filename)
		if f.PreviousFilename != "" {
			files = append(files, f.PreviousFilename)
		}
	}
	return files, nil
}

// escapePath escapes each segment of a slash-separated name such as a branch.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
