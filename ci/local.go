package ci

import (
	"fmt"
	"io"

	"github.com/aloutfi/actions/vcs"
)

// Local is the Null CI: a laptop or an unsupported CI. It knows nothing about
// the run (Attempt 0 means "unknown", which every policy treats as a possible
// re-run). Step outputs go to $TFPR_OUTPUT_FILE, or stdout when
// it is unset, and logs go to stderr.
type Local struct {
	env         Env
	out, stderr io.Writer
}

func newLocal(env Env, stdout, stderr io.Writer, _ *vcs.Client) CI {
	return &Local{env: env, out: stdout, stderr: stderr}
}

// Context is empty: a local run has no run id, commit or PR.
func (l *Local) Context() Ctx { return Ctx{} }

// JobURL is unknown.
func (l *Local) JobURL() string { return "" }

// SetOutput writes name=value to $TFPR_OUTPUT_FILE or stdout.
func (l *Local) SetOutput(name, value string) error {
	if path := l.env("TFPR_OUTPUT_FILE"); path != "" {
		return appendOutput(path, name, value)
	}
	_, err := fmt.Fprintf(l.out, "%s=%s\n", name, value)
	return err
}

// Log prints "<level>: msg" on stderr.
func (l *Local) Log(level, msg string) {
	fmt.Fprintf(l.stderr, "%s: %s\n", level, msg)
}
