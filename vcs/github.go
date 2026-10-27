package vcs

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aloutfi/actions/redact"
)

// DefaultGitHubAuthor is the login of the default GITHUB_TOKEN identity.
const DefaultGitHubAuthor = "github-actions[bot]"

// NewGitHubClient is a REST client for the GitHub API at apiURL
// (https://api.github.com when empty), authenticated with token if set.
func NewGitHubClient(apiURL, token string) *Client {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
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

// NewGitHub returns the adapter for repo (owner/repo). Only comments by author
// (default github-actions[bot]) are ever edited.
func NewGitHub(api *Client, repo, author string) *GitHub {
	if author == "" {
		author = DefaultGitHubAuthor
	}
	return &GitHub{api: api, repo: repo, author: author}
}

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// UpsertComment edits the first comment by the adapter's author that starts
// with marker, or posts a new one. A human who pastes the marker into their
// own comment never has it overwritten.
func (g *GitHub) UpsertComment(pr, marker string, body redact.Text) (string, error) {
	var id int64
	// If the listing fails, tfpr cannot know whether its comment exists, and
	// posting could duplicate it, so it gives up instead.
	err := GetPages(g.api, fmt.Sprintf("/repos/%s/issues/%s/comments?per_page=100", g.repo, url.PathEscape(pr)),
		func(page []issueComment) bool {
			for _, c := range page {
				if c.User.Login == g.author && strings.HasPrefix(c.Body, marker) {
					id = c.ID
					return false
				}
			}
			return true
		})
	if err != nil {
		return "", fmt.Errorf("list comments on PR #%s: %w", pr, err)
	}
	payload := map[string]string{"body": body.String()}
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

// PRsForCommit lists the PRs associated with sha (first page only).
func (g *GitHub) PRsForCommit(sha string) ([]PR, error) {
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

// escapePath escapes each segment of a slash-separated name such as a branch.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
