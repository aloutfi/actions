// Package ci adapts CI platforms (where the job runs) to one small port, and
// detects which platform tfpr is running on.
package ci

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aloutfi/actions/redact"
	"github.com/aloutfi/actions/vcs"
)

// Log levels.
const (
	Notice  = "notice"
	Warning = "warning"
	Error   = "error"
)

// Ctx is what tfpr needs to know about the current run.
type Ctx struct {
	CI            string // adapter name: github, azure, local
	RunID, RunURL string
	SHA           string // the commit checked out and built (on PR builds, the merge commit)
	HeadSHA       string // PR builds: the PR's head commit
	Ref           string // full ref, e.g. refs/heads/main
	PR            string // PR number for pull-request builds, else ""
	Repo          string // repo id the VCS adapter understands
	VCS           string // the repo host this CI says the repo lives on: github, azure; "" unknown
	VCSURL        string // that host's API base URL, when the CI knows it
	VCSPrivate    bool   // the host is private (GitHub Enterprise): never fall back to a public API
	CIAPI         string // the CI's own REST API base, when it has one (the Azure DevOps collection)
	Actor         string
	Attempt       int // 1 on the first attempt; 0 when the CI has no counter
}

// CI is the CI-platform port: read the run context, write step outputs, log.
type CI interface {
	Context() Ctx
	// JobURL links to the current job's logs, falling back to the run URL.
	JobURL() string
	SetOutput(name, value string) error
	// Log writes msg at level, escaped so it is one log line of data.
	Log(level, msg string)
	// Inert returns a writer to w whose content the CI will not interpret as
	// commands, for echoing text tfpr does not control (terraform output, a
	// rendered comment). Close ends the inert region; it does not close w.
	Inert(w io.Writer) io.WriteCloser
	// RunFor links, best effort, to the run of this pipeline that built
	// sha; failing that, to the commit; failing that, "".
	RunFor(sha string) string
	// UploadSummary publishes markdown as the job's summary page. Only the
	// redaction filter makes a redact.Text.
	UploadSummary(markdown redact.Text) error
}

// SummaryLinker is a CI whose job summary has a URL of its own. A CI without
// one gets no summary link in the comment.
type SummaryLinker interface {
	SummaryURL() string
}

// Tagger is a CI that can tag the run (Azure Pipelines build tags).
type Tagger interface {
	AddBuildTag(tag string) error
}

// PartialSucceeder is a CI that can end a step as "succeeded with issues"
// (Azure Pipelines). Others have only pass and fail.
type PartialSucceeder interface {
	CompleteWithIssues(msg string) error
}

// Env reads an environment variable; os.Getenv in production.
type Env func(string) string

// APIs are the REST clients CI adapters may call: the one GitHub client and
// the one Azure DevOps client, shared with the repo-host adapters.
type APIs struct{ GitHub, Azure *vcs.Client }

// Detect returns the adapter for the CI platform described by env: GitHub
// Actions, Azure Pipelines, or Local when nothing else matches. Output and
// logs go to stdout and stderr.
func Detect(env Env, stdout, stderr io.Writer, apis APIs) CI {
	switch {
	case env("GITHUB_ACTIONS") == "true":
		return newGitHub(env, stdout, stderr, apis.GitHub)
	case strings.EqualFold(env("TF_BUILD"), "true"):
		return newAzure(env, stdout, stderr, apis.Azure)
	}
	return newLocal(env, stdout, stderr, nil)
}

// appendOutput appends a name=value line to the step-output file at path.
func appendOutput(path, name, value string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%s=%s\n", name, value); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
