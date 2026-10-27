// Package vcs adapts repo hosts (where the pull request lives) to one small
// port, plus the shared REST client the adapters use.
package vcs

import "fmt"

// PR is a pull request associated with a commit.
type PR struct {
	Number  int
	HeadSHA string
}

// VCS is the repo-host port. Identity scoping (whose comments may be
// edited) lives inside the adapter.
type VCS interface {
	// UpsertComment updates the adapter's own comment on pr whose body starts
	// with marker, or posts a new one. It returns a line describing what it did.
	UpsertComment(pr, marker, body string) (string, error)
	// PRsForCommit lists the PRs associated with sha, such as the PR that merged it.
	PRsForCommit(sha string) ([]PR, error)
	// BranchHead is the current head commit of branch (short name).
	BranchHead(branch string) (string, error)
}

// Config selects and configures a repo-host adapter.
type Config struct {
	Name   string // adapter name; "" picks github, or none without a repo
	Repo   string // host-specific repo id (owner/repo on GitHub)
	API    *Client
	Author string // login that owns tfpr's comments; "" is the host default
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
		return NewGitHub(cfg.API, cfg.Repo, cfg.Author), nil
	case "none":
		return NoPR{}, nil
	}
	return nil, fmt.Errorf("unknown TFPR_VCS '%s'", cfg.Name)
}
