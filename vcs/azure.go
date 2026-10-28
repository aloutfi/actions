package vcs

import (
	"encoding/json"
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
// thread. Thread properties are write-once (a PATCH that sets any fails
// with 400), so a property carries only the marker, which never changes. The
// record and tfpr's own state (fingerprint, the status it last set) are the
// first comment's last two lines, hidden; only the author can edit a
// comment. Threads written before that carry them in properties, which are
// still read. Only threads whose first comment the build identity wrote (the
// identity from connectionData, project- or collection-scoped) are ever
// touched.
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

// Thread property names. Only the marker is written; the others are read
// from threads written before the record moved into the comment.
const (
	propMarker      = "tfpr.marker"
	propRecord      = "tfpr.record"
	propFingerprint = "tfpr.fingerprint"
	propStatus      = "tfpr.status" // the status tfpr itself last set
)

// threadState is tfpr's own state on a thread, the first comment's last line.
type threadState struct {
	Fingerprint string `json:"fp,omitempty"`
	Status      string `json:"status,omitempty"` // the status tfpr itself last set
}

const statePrefix, stateSuffix = "<!-- tfpr:thread ", " -->"

// StateOverhead is the most characters Azure Repos adds to a comment body
// with record: a blank line, the record line and the state line.
func StateOverhead(record string) int {
	return 2 + len([]rune(record)) + 1 + len(statePrefix) + len(stateSuffix) + 120
}

// content is the first comment's text: body, then the record (if any) and
// the state, last, where nothing from the body can follow them.
func content(body, record string, st threadState) string {
	data, _ := json.Marshal(st)
	tail := statePrefix + string(data) + stateSuffix
	if record != "" {
		tail = record + "\n" + tail
	}
	return body + "\n\n" + tail
}

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
		ID      int64  `json:"id"`
		Content string `json:"content"`
		Author  struct {
			ID string `json:"id"`
		} `json:"author"`
	} `json:"comments"`
}

func (t adoThread) prop(name string) string {
	s, _ := t.Properties[name].Value.(string)
	return s
}

// tail is the record and state tfpr last wrote on the thread: the first
// comment's last two lines, else (threads written before) its properties.
func (t adoThread) tail() (record string, st threadState) {
	lines := strings.Split(t.Comments[0].Content, "\n")
	last := lines[len(lines)-1]
	if js, ok := strings.CutPrefix(last, statePrefix); ok {
		if js, ok = strings.CutSuffix(js, stateSuffix); ok && json.Unmarshal([]byte(js), &st) == nil {
			if len(lines) > 1 && strings.HasPrefix(lines[len(lines)-2], "<!-- tfpr:plan@") {
				record = lines[len(lines)-2]
			}
			return record, st
		}
	}
	return t.prop(propRecord), threadState{Fingerprint: t.prop(propFingerprint), Status: t.prop(propStatus)}
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

	status, reopened := z.status(c, found)
	st := threadState{Fingerprint: c.Fingerprint, Status: status}
	if status == "" { // unchanged: tfpr's last status stands
		_, prev := found.tail()
		st.Status = prev.Status
	}
	text := content(c.Body.String(), c.Record, st)

	if found == nil {
		thread := map[string]any{
			"comments":   []map[string]any{{"parentCommentId": 0, "content": text, "commentType": 1}},
			"status":     status,
			"properties": map[string]typedValue{propMarker: str(c.Marker)},
		}
		if _, err := z.api.Do(http.MethodPost, z.path("/pullRequests/%s/threads", url.PathEscape(c.PR)), thread, nil); err != nil {
			return "", err
		}
		return "Posted thread on PR #" + c.PR, nil
	}

	first := found.Comments[0].ID
	threadPath := fmt.Sprintf("/pullRequests/%s/threads/%d", url.PathEscape(c.PR), found.ID)
	if _, err := z.api.Do(http.MethodPatch, z.path("%s/comments/%d", threadPath, first), map[string]any{"content": text}, nil); err != nil {
		return "", err
	}
	if reopened {
		reply := map[string]any{"parentCommentId": first, "content": ackReply, "commentType": 1}
		if _, err := z.api.Do(http.MethodPost, z.path("%s/comments", threadPath), reply, nil); err != nil {
			return "", err
		}
	}
	// Status only: a thread PATCH that carries properties fails as a whole.
	if status != "" {
		if _, err := z.api.Do(http.MethodPatch, z.path("%s", threadPath), map[string]any{"status": status}, nil); err != nil {
			return "", err
		}
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
		rec, _ := t.tail()
		records = append(records, rec)
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
	}
	_, st := existing.tail()
	if st.Fingerprint == c.Fingerprint {
		return "", false // unchanged: keep the reviewer's resolution
	}
	acknowledged := resolutions[existing.Status] && existing.Status != st.Status
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

// adoDiffPage and adoDiffMax bound ChangedFiles: changes per request, and in
// all.
const adoDiffPage, adoDiffMax = 1000, 20000

// ChangedFiles lists the files changed from base to head with the commit
// diffs API, a page at a time. A base the head is not a descendant of
// (behindCount above 0, as after a force-push) is an error.
func (z *Azure) ChangedFiles(base, head string) ([]string, error) {
	var files []string
	for skip := 0; ; {
		var d struct {
			AllChangesIncluded bool `json:"allChangesIncluded"`
			BehindCount        int  `json:"behindCount"`
			Changes            []struct {
				Item struct {
					Path         string `json:"path"`
					OriginalPath string `json:"originalPath"` // a rename's old path
				} `json:"item"`
				SourceServerItem string `json:"sourceServerItem"`
			} `json:"changes"`
		}
		// diffCommonCommit=false: the trees of base and head themselves, not
		// head against a merge base, which hides what a force-push dropped.
		p := z.path("/diffs/commits?baseVersionType=commit&baseVersion=%s&targetVersionType=commit&targetVersion=%s&diffCommonCommit=false&$top=%d&$skip=%d",
			url.QueryEscape(base), url.QueryEscape(head), adoDiffPage, skip)
		if _, err := z.api.Do(http.MethodGet, p, nil, &d); err != nil {
			return nil, err
		}
		if d.BehindCount > 0 {
			return nil, fmt.Errorf("%s is not an ancestor of %s (%d commits behind)", base, head, d.BehindCount)
		}
		for _, c := range d.Changes {
			for _, p := range []string{c.Item.Path, c.Item.OriginalPath, c.SourceServerItem} {
				if p != "" {
					files = append(files, strings.TrimPrefix(p, "/"))
				}
			}
		}
		if d.AllChangesIncluded || len(d.Changes) == 0 {
			return files, nil
		}
		if skip += len(d.Changes); skip >= adoDiffMax {
			return nil, fmt.Errorf("more than %d changes from %s to %s", adoDiffMax, base, head)
		}
	}
}
