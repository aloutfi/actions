// Package ci adapts CI platforms (where the job runs) to one small port, and
// detects which platform tfpr is running on.
package ci

import (
	"fmt"
	"io"
	"os"

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
	RunID, RunURL string
	SHA           string // commit being built
	Ref           string // full ref, e.g. refs/heads/main
	PR            string // PR number for pull-request builds, else ""
	Repo          string // repo id the VCS adapter understands
	Actor         string
	Attempt       int // 1 on the first attempt; 0 when the CI has no counter
}

// CI is the CI-platform port: read the run context, write step outputs, log.
type CI interface {
	Context() Ctx
	// JobURL links to the current job's logs, falling back to the run URL.
	JobURL() string
	SetOutput(name, value string) error
	Log(level, msg string)
}

// Env reads an environment variable; os.Getenv in production.
type Env func(string) string

// detectors is ordered: the first match wins, and local always matches.
var detectors = []struct {
	name  string
	match func(Env) bool
	new   func(Env, io.Writer, io.Writer, *vcs.Client) CI
}{
	{"github", func(e Env) bool { return e("GITHUB_ACTIONS") == "true" }, newGitHub},
	{"local", func(Env) bool { return true }, newLocal},
}

// Detect returns the adapter for the CI platform described by env. Output
// and logs go to stdout and stderr; api serves platforms that need REST calls.
func Detect(env Env, stdout, stderr io.Writer, api *vcs.Client) CI {
	for _, d := range detectors {
		if d.match(env) {
			return d.new(env, stdout, stderr, api)
		}
	}
	panic("unreachable: local always matches")
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
