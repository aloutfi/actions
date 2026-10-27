package ci

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aloutfi/actions/redact"
	"github.com/aloutfi/actions/vcs"
)

// Azure is Azure Pipelines. It talks to the agent through logging commands
// (##vso[...]) on stdout.
type Azure struct {
	env Env
	out io.Writer
}

func newAzure(env Env, stdout, stderr io.Writer, _ *vcs.Client) CI {
	a := &Azure{env: env, out: stdout}
	// Which commits a PR build sees is an open verification item: log both.
	// stderr, because some commands' stdout is data.
	if head, merge := env("SYSTEM_PULLREQUEST_SOURCECOMMITID"), env("BUILD_SOURCEVERSION"); head != "" {
		fmt.Fprintf(stderr, "tfpr: Azure Pipelines PR build: planned commit Build.SourceVersion=%s (the merge commit checked out); PR head System.PullRequest.SourceCommitId=%s.\n",
			neutralize(merge), neutralize(head))
	}
	return a
}

// Context reads the run from the Azure Pipelines environment.
//
// SHA is the commit checked out and built, Build.SourceVersion (on PR
// builds, the merge commit), as on GitHub Actions; HeadSHA is the PR head,
// System.PullRequest.SourceCommitId. The repo host follows
// Build.Repository.Provider: Azure Repos (TfsGit) or GitHub.
func (a *Azure) Context() Ctx {
	e := a.env
	attempt, _ := strconv.Atoi(e("SYSTEM_JOBATTEMPT")) // missing: 0, unknown
	c := Ctx{
		CI:      "azure",
		RunID:   e("BUILD_BUILDID"),
		RunURL:  a.runURL(),
		SHA:     e("BUILD_SOURCEVERSION"),
		Ref:     e("BUILD_SOURCEBRANCH"),
		Actor:   e("BUILD_REQUESTEDFOR"),
		Attempt: attempt,
	}
	c.HeadSHA = e("SYSTEM_PULLREQUEST_SOURCECOMMITID")
	switch e("BUILD_REPOSITORY_PROVIDER") {
	case "TfsGit":
		c.VCS, c.VCSURL = "azure", e("SYSTEM_COLLECTIONURI")
		c.Repo = e("SYSTEM_TEAMPROJECTID") + "/" + e("BUILD_REPOSITORY_ID")
		c.PR = e("SYSTEM_PULLREQUEST_PULLREQUESTID")
	case "GitHub", "GitHubEnterprise":
		// On GitHub repos PullRequestId is Azure's internal id; the number
		// GitHub knows is PullRequestNumber.
		c.VCS, c.Repo, c.PR = "github", e("BUILD_REPOSITORY_NAME"), e("SYSTEM_PULLREQUEST_PULLREQUESTNUMBER")
		if e("BUILD_REPOSITORY_PROVIDER") == "GitHubEnterprise" {
			// The enterprise API lives on the repository's host. Without it,
			// tfpr must not guess api.github.com and send it the token.
			c.VCSPrivate = true
			if u, err := url.Parse(e("BUILD_REPOSITORY_URI")); err == nil && u.Scheme == "https" && u.Host != "" {
				c.VCSURL = "https://" + u.Host + "/api/v3"
			}
		}
	}
	return c
}

func (a *Azure) runURL() string {
	base := a.env("SYSTEM_COLLECTIONURI")
	if base == "" {
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/" + url.PathEscape(a.env("SYSTEM_TEAMPROJECT")) +
		"/_build/results?buildId=" + url.QueryEscape(a.env("BUILD_BUILDID"))
}

// JobURL links to this job's logs.
func (a *Azure) JobURL() string {
	u := a.runURL()
	if j := a.env("SYSTEM_JOBID"); u != "" && j != "" {
		u += "&view=logs&j=" + url.QueryEscape(j)
	}
	return u
}

// SetOutput sets an output variable that later jobs and stages can read
// (isOutput=true; reference it through the step's name).
//
// The value is also neutralized (escapeMessage), so a value can never carry
// a "##vso[" command. That's deliberately fail-safe: outputs are counts,
// flags and names, which never contain one legitimately.
func (a *Azure) SetOutput(name, value string) error {
	_, err := fmt.Fprintf(a.out, "##vso[task.setvariable variable=%s;isOutput=true]%s\n", escapeProperty(name), escapeMessage(value))
	return err
}

// Log writes a warning or error as a log issue, with the message escaped as
// command data. Anything else is a plain line: not escaped (it is no command
// data), only neutralized so it cannot become a command.
func (a *Azure) Log(level, msg string) {
	switch level {
	case Warning, Error:
		fmt.Fprintf(a.out, "##vso[task.logissue type=%s]%s\n", level, escapeMessage(msg))
	default:
		fmt.Fprintln(a.out, neutralize(msg))
	}
}

// Inert neutralizes logging and formatting commands in echoed text: Azure
// Pipelines has no stop-commands, and the agent reads "##vso[" anywhere in a
// line. Every "##vso[" and "##[" is broken up as "# #vso[" and "# #[", which
// a reader still recognizes and the agent ignores.
func (a *Azure) Inert(w io.Writer) io.WriteCloser { return &neutralizer{w: w} }

// UploadSummary attaches markdown to the run's Extensions tab
// (task.uploadsummary). The markdown must have been through the filter.
func (a *Azure) UploadSummary(markdown redact.Text) error {
	dir := a.env("AGENT_TEMPDIRECTORY")
	if dir == "" {
		dir = os.TempDir()
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	path := filepath.Join(dir, "tfpr-summary-"+hex.EncodeToString(b[:])+".md")
	if err := os.WriteFile(path, []byte(markdown.String()), 0o644); err != nil {
		return err
	}
	_, err := fmt.Fprintf(a.out, "##vso[task.uploadsummary]%s\n", escapeMessage(path))
	return err
}

// AddBuildTag tags the run (build.addbuildtag), e.g. "drift".
func (a *Azure) AddBuildTag(tag string) error {
	_, err := fmt.Fprintf(a.out, "##vso[build.addbuildtag]%s\n", escapeMessage(tag))
	return err
}

// CompleteWithIssues ends the step as SucceededWithIssues, e.g. when drift is
// found and failing on drift is enabled.
func (a *Azure) CompleteWithIssues(msg string) error {
	_, err := fmt.Fprintf(a.out, "##vso[task.complete result=SucceededWithIssues;]%s\n", escapeMessage(msg))
	return err
}

// escapeMessage escapes the data part of a logging command, as the agent's
// task library does, and breaks up any command inside it.
func escapeMessage(s string) string {
	return neutralize(strings.NewReplacer("%", "%AZP25", "\r", "%0D", "\n", "%0A").Replace(s))
}

// escapeProperty escapes a property value of a logging command.
func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%AZP25", "\r", "%0D", "\n", "%0A", ";", "%3B", "]", "%5D").Replace(s)
}

var commandBreaker = strings.NewReplacer("##vso[", "# #vso[", "##[", "# #[")

func neutralize(s string) string { return commandBreaker.Replace(s) }

// neutralizer rewrites whole lines, so a command split across two writes is
// still caught; Close flushes a final partial line.
type neutralizer struct {
	w   io.Writer
	buf []byte
}

func (n *neutralizer) Write(p []byte) (int, error) {
	n.buf = append(n.buf, p...)
	if i := bytes.LastIndexByte(n.buf, '\n'); i >= 0 {
		if _, err := io.WriteString(n.w, neutralize(string(n.buf[:i+1]))); err != nil {
			return 0, err
		}
		n.buf = n.buf[i+1:]
	}
	return len(p), nil
}

func (n *neutralizer) Close() error {
	if len(n.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(n.w, neutralize(string(n.buf)))
	n.buf = nil
	return err
}
