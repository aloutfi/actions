// Package vcs adapts repo hosts (where the pull request lives) to one small
// port, plus the shared REST client the adapters use.
package vcs

import (
	"fmt"
	"strings"

	"github.com/aloutfi/actions/redact"
)

// PR is a pull request associated with a commit.
type PR struct {
	Number  int
	HeadSHA string
}

// Comment is one tfpr comment on a PR: a comment on GitHub, a thread on
// Azure Repos.
type Comment struct {
	PR     string
	Marker string // finds the comment again; tfpr's own, never filtered
	Record string // the comment record; tfpr's own, never filtered; "" for none
	// Body is the human-readable part. Only the redaction filter makes one.
	Body redact.Text
	// Resolved means there is nothing to review: a no-op plan or a
	// successful apply. Hosts with thread status close the thread.
	Resolved bool
	// Fingerprint identifies what was planned. With the ack option, a
	// changed fingerprint reopens a thread a reviewer had resolved.
	Fingerprint string
}

// VCS is the repo-host port. Identity scoping (whose comments may be
// edited) lives inside the adapter.
type VCS interface {
	// UpsertComment updates the adapter's own comment for c.Marker on c.PR,
	// or posts a new one. It returns a line describing what it did. If the
	// existing comments can't be listed, it fails rather than risk a
	// duplicate. Hosts that can store the marker and record out of band
	// (thread properties) do; others put them above the body.
	UpsertComment(c Comment) (string, error)
	// Records returns the comment records (Record lines) on pr's comments
	// for marker that the adapter's own identity wrote; "" for such a
	// comment without one. Comments by anyone else are ignored, so a PR
	// participant can't forge a record.
	Records(pr, marker string) ([]string, error)
	// PRsForCommit lists the PRs associated with sha, such as the PR that
	// merged it into ref (the full ref being built, e.g. refs/heads/main).
	PRsForCommit(sha, ref string) ([]PR, error)
	// BranchHead is the current head commit of branch (short name).
	BranchHead(branch string) (string, error)
}

// Config selects and configures a repo-host adapter.
type Config struct {
	Name   string  // adapter name: github, azure, none; "" picks github, or none without a repo
	Repo   string  // host-specific repo id: owner/repo on GitHub, <project>/<repository> on Azure Repos
	GitHub *Client // github: the one GitHub REST client, shared with the CI adapter
	Azure  *Client // azure: the one Azure DevOps client (the collection), shared with the CI adapter
	Author string  // GitHub: the login that owns tfpr's comments; "" is the default
	Ack    bool    // Azure Repos: plan threads wait for a reviewer (see Azure)
}

// New returns the adapter named by cfg.Name.
func New(cfg Config) (VCS, error) {
	name := cfg.Name
	if name == "" {
		name = "github"
		if cfg.Repo == "" {
			name = "none"
		}
	}
	switch name {
	case "github":
		if cfg.GitHub == nil {
			return nil, fmt.Errorf("no GitHub client")
		}
		return NewGitHub(cfg.GitHub, cfg.Repo, cfg.Author), nil
	case "azure":
		if cfg.Azure == nil || cfg.Azure.BaseURL == "" || !strings.Contains(cfg.Repo, "/") {
			return nil, fmt.Errorf("Azure Repos needs the collection URL and <project>/<repository> (got repository '%s')", cfg.Repo)
		}
		return NewAzure(cfg.Azure, cfg.Repo, cfg.Ack), nil
	case "none":
		return NoPR{}, nil
	}
	return nil, fmt.Errorf("unknown TFPR_VCS '%s'", cfg.Name)
}
