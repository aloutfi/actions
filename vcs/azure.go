package vcs

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// NewAzureClient is a REST client for an Azure DevOps collection
// (System.CollectionUri), authenticated with token (the build's
// System.AccessToken) if set.
func NewAzureClient(collectionURI, token string) *Client {
	h := map[string]string{"Accept": "application/json", "User-Agent": "tfpr"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return &Client{BaseURL: strings.TrimSuffix(collectionURI, "/"), Header: h}
}

// Azure is Azure Repos, over the REST API (7.1). Each stack gets one PR
// thread. The thread's properties carry tfpr's marker, record and
// fingerprint, so the comment text needs no hidden markup. Only threads whose
// first comment the build identity wrote (the identity from connectionData,
// project- or collection-scoped) are ever touched.
//
// Thread status: a resolved comment (no-op plan, apply result) is closed and
// anything else is active, so a "Check for comment resolution" branch policy
// makes reviewers resolve real plans. With ack, a re-plan keeps a reviewer's
// resolution while the plan is unchanged, and reopens the thread with a reply
// when it changes.
type Azure struct {
	api      *Client
	project  string
	repo     string
	ack      bool
	identity string // build identity id, once looked up
}

// NewAzure returns the adapter for repo, "<project>/<repository>" (ids or
// names).
func NewAzure(api *Client, repo string, ack bool) *Azure {
	project, name, _ := strings.Cut(repo, "/")
	return &Azure{api: api, project: project, repo: name, ack: ack}
}

// Thread property names.
const (
	propMarker      = "tfpr.marker"
	propRecord      = "tfpr.record"
	propFingerprint = "tfpr.fingerprint"
	propStatus      = "tfpr.status" // the status tfpr itself last set
)

// resolutions are the statuses a reviewer resolves a thread with.
var resolutions = map[string]bool{"fixed": true, "closed": true, "byDesign": true, "wontFix": true}

// ackReply is posted when a plan changes after a reviewer resolved its thread.
const ackReply = "The plan changed since it was acknowledged. Review the updated plan above and resolve this thread again."

// path is a repository API path with the API version.
func (z *Azure) path(format string, args ...any) string {
	p := fmt.Sprintf("/%s/_apis/git/repositories/%s", url.PathEscape(z.project), url.PathEscape(z.repo)) + fmt.Sprintf(format, args...)
	sep := "?"
	if strings.Contains(p, "?") {
		sep = "&"
	}
	return p + sep + "api-version=7.1"
}

// me is the build identity's id.
func (z *Azure) me() (string, error) {
	if z.identity != "" {
		return z.identity, nil
	}
	var cd struct {
		AuthenticatedUser struct {
			ID string `json:"id"`
		} `json:"authenticatedUser"`
	}
	if _, err := z.api.Do(http.MethodGet, "/_apis/connectionData", nil, &cd); err != nil {
		return "", fmt.Errorf("look up the build identity: %w", err)
	}
	if cd.AuthenticatedUser.ID == "" {
		return "", errors.New("look up the build identity: connectionData has no authenticated user")
	}
	z.identity = cd.AuthenticatedUser.ID
	return z.identity, nil
}

type typedValue struct {
	Type  string `json:"$type"`
	Value any    `json:"$value"`
}

type adoThread struct {
	ID         int64                 `json:"id"`
	Status     string                `json:"status"`
	IsDeleted  bool                  `json:"isDeleted"`
	Properties map[string]typedValue `json:"properties"`
	Comments   []struct {
		ID     int64 `json:"id"`
		Author struct {
			ID string `json:"id"`
		} `json:"author"`
	} `json:"comments"`
}

func (t adoThread) prop(name string) string {
	s, _ := t.Properties[name].Value.(string)
	return s
}

// UpsertComment updates the build identity's thread for c.Marker on c.PR,
// or creates one.
func (z *Azure) UpsertComment(c Comment) (string, error) {
	me, err := z.me()
	if err != nil {
		return "", err
	}
	// If the listing fails, tfpr cannot know whether its thread exists, and
	// creating one could duplicate it, so it gives up instead.
	own, err := z.own(c.PR, c.Marker, me)
	if err != nil {
		return "", err
	}
	var found *adoThread
	if len(own) > 0 {
		found = &own[0]
	}

	props := map[string]typedValue{propMarker: str(c.Marker)}
	if c.Record != "" {
		props[propRecord] = str(c.Record)
	}
	if c.Fingerprint != "" {
		props[propFingerprint] = str(c.Fingerprint)
	}
	status, reopened := z.status(c, found)
	if status != "" {
		props[propStatus] = str(status)
	}

	if found == nil {
		thread := map[string]any{
			"comments":   []map[string]any{{"parentCommentId": 0, "content": c.Body.String(), "commentType": 1}},
			"status":     status,
			"properties": props,
		}
		if _, err := z.api.Do(http.MethodPost, z.path("/pullRequests/%s/threads", url.PathEscape(c.PR)), thread, nil); err != nil {
			return "", err
		}
		return "Posted thread on PR #" + c.PR, nil
	}

	first := found.Comments[0].ID
	threadPath := fmt.Sprintf("/pullRequests/%s/threads/%d", url.PathEscape(c.PR), found.ID)
	if _, err := z.api.Do(http.MethodPatch, z.path("%s/comments/%d", threadPath, first), map[string]any{"content": c.Body.String()}, nil); err != nil {
		return "", err
	}
	if reopened {
		reply := map[string]any{"parentCommentId": first, "content": ackReply, "commentType": 1}
		if _, err := z.api.Do(http.MethodPost, z.path("%s/comments", threadPath), reply, nil); err != nil {
			return "", err
		}
	}
	update := map[string]any{"properties": props}
	if status != "" {
		update["status"] = status
	}
	if _, err := z.api.Do(http.MethodPatch, z.path("%s", threadPath), update, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("Updated thread %d on PR #%s", found.ID, c.PR), nil
}

// own lists the threads on pr for marker whose first comment me wrote.
func (z *Azure) own(pr, marker, me string) ([]adoThread, error) {
	var threads struct {
		Value []adoThread `json:"value"`
	}
	if _, err := z.api.Do(http.MethodGet, z.path("/pullRequests/%s/threads", url.PathEscape(pr)), nil, &threads); err != nil {
		return nil, fmt.Errorf("list threads on PR #%s: %w", pr, err)
	}
	var own []adoThread
	for _, t := range threads.Value {
		if !t.IsDeleted && t.prop(propMarker) == marker && len(t.Comments) > 0 && t.Comments[0].Author.ID == me {
			own = append(own, t)
		}
	}
	return own, nil
}

// Records reads the record property of every thread of the build identity
// for marker.
func (z *Azure) Records(pr, marker string) ([]string, error) {
	me, err := z.me()
	if err != nil {
		return nil, err
	}
	own, err := z.own(pr, marker, me)
	if err != nil {
		return nil, err
	}
	records := make([]string, 0, len(own))
	for _, t := range own {
		records = append(records, t.prop(propRecord))
	}
	return records, nil
}

// status is the thread status to set ("" leaves it alone), and whether a
// reviewer's resolution is being undone because the plan changed. A
// resolution counts only if a reviewer made it: the thread has a resolution
// status that tfpr did not set itself (tfpr closes no-op threads; "pending"
// is no resolution).
func (z *Azure) status(c Comment, existing *adoThread) (status string, reopened bool) {
	switch {
	case c.Resolved:
		return "closed", false
	case !z.ack || existing == nil:
		return "active", false
	case existing.prop(propFingerprint) == c.Fingerprint:
		return "", false // unchanged: keep the reviewer's resolution
	}
	acknowledged := resolutions[existing.Status] && existing.Status != existing.prop(propStatus)
	return "active", acknowledged
}

func str(s string) typedValue { return typedValue{Type: "System.String", Value: s} }

type adoPR struct {
	ID                    int    `json:"pullRequestId"`
	Status                string `json:"status"`
	TargetRefName         string `json:"targetRefName"`
	LastMergeSourceCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
	LastMergeCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeCommit"`
}

// mergedPR matches the title Azure Repos gives a PR's merge commit, which is
// all a squash merge leaves to go on.
var mergedPR = regexp.MustCompile(`^Merged PR (\d+):`)

// PRsForCommit finds the PR that sha merged into ref: by its last merge
// commit (pullrequestquery), or else by the "Merged PR <id>:" title of a
// squash merge. Anyone can write such a title, so that PR counts only if it
// is completed, targeted ref, and its last merge commit is sha. Otherwise
// there is no PR: never the wrong one.
func (z *Azure) PRsForCommit(sha, ref string) ([]PR, error) {
	var q struct {
		Results []map[string][]adoPR `json:"results"`
	}
	query := map[string]any{"queries": []map[string]any{{"type": "lastMergeCommit", "items": []string{sha}}}}
	if _, err := z.api.Do(http.MethodPost, z.path("/pullrequestquery"), query, &q); err != nil {
		return nil, err
	}
	var prs []PR
	for _, r := range q.Results {
		for _, p := range r[sha] {
			prs = append(prs, PR{Number: p.ID, HeadSHA: p.LastMergeSourceCommit.CommitID})
		}
	}
	if len(prs) > 0 {
		return prs, nil
	}

	var commit struct {
		Comment string `json:"comment"`
	}
	if _, err := z.api.Do(http.MethodGet, z.path("/commits/%s", url.PathEscape(sha)), nil, &commit); err != nil {
		return nil, err
	}
	m := mergedPR.FindStringSubmatch(commit.Comment)
	if m == nil {
		return nil, nil
	}
	id, _ := strconv.Atoi(m[1])
	var pr adoPR
	if _, err := z.api.Do(http.MethodGet, z.path("/pullrequests/%d", id), nil, &pr); err != nil {
		return nil, err
	}
	if pr.Status != "completed" || ref == "" || pr.TargetRefName != ref || pr.LastMergeCommit.CommitID != sha {
		return nil, nil
	}
	return []PR{{Number: pr.ID, HeadSHA: pr.LastMergeSourceCommit.CommitID}}, nil
}

// BranchHead is the commit at the tip of branch.
func (z *Azure) BranchHead(branch string) (string, error) {
	var refs struct {
		Value []struct {
			Name     string `json:"name"`
			ObjectID string `json:"objectId"`
		} `json:"value"`
	}
	if _, err := z.api.Do(http.MethodGet, z.path("/refs?filter=%s", url.QueryEscape("heads/"+branch)), nil, &refs); err != nil {
		return "", err
	}
	for _, r := range refs.Value { // filter is a prefix match
		if r.Name == "refs/heads/"+branch {
			return r.ObjectID, nil
		}
	}
	return "", fmt.Errorf("no head commit for branch '%s'", branch)
}
