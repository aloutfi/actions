package vcs

import (
	"errors"

	"github.com/aloutfi/actions/redact"
)

// NoPR is the Null repo host, used when there is no repository to talk to
// (for example a local run). It never touches the network. BranchHead fails,
// so the superseded-tree guard refuses a fresh apply on a branch.
type NoPR struct{}

// UpsertComment skips the comment.
func (NoPR) UpsertComment(pr, _ string, _ redact.Text) (string, error) {
	return "No repository host configured; skipping the comment on PR #" + pr + ".", nil
}

// PRsForCommit finds no PRs.
func (NoPR) PRsForCommit(string) ([]PR, error) { return nil, nil }

// BranchHead cannot know the head.
func (NoPR) BranchHead(string) (string, error) {
	return "", errors.New("no repository host configured")
}
