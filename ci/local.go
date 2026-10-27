package ci

import (
	"fmt"
	"io"
	"os"

	"github.com/aloutfi/actions/redact"
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

// Context is empty apart from the name: a local run has no run id, commit
// or PR.
func (l *Local) Context() Ctx { return Ctx{CI: "local"} }

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

// Inert passes text through: nothing reads commands from a local run.
func (l *Local) Inert(w io.Writer) io.WriteCloser { return nopCloser{w} }

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// UploadSummary writes markdown to $TFPR_SUMMARY_FILE, if set; a local run
// has no summary page otherwise.
func (l *Local) UploadSummary(markdown redact.Text) error {
	path := l.env("TFPR_SUMMARY_FILE")
	if path == "" {
		return nil
	}
	return os.WriteFile(path, []byte(markdown.String()), 0o644)
}

// Log prints "<level>: msg" on stderr.
func (l *Local) Log(level, msg string) {
	fmt.Fprintf(l.stderr, "%s: %s\n", level, msg)
}
