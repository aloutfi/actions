// Package redact runs the configured redaction filter over every piece of
// text tfpr sends anywhere: terraform output it echoes, the plan text it
// stores, and every comment body it posts.
//
// The filter is the command in TFPR_FILTER, run with `sh -c`: it reads text on
// stdin and writes the redacted text on stdout. It fails closed: a non-zero
// exit, a timeout, or empty output for non-empty input is an error, and the
// caller sends nothing.
//
// Apply also strips CI command lines (lines starting with "##vso[" or "::")
// from the text, whether a filter is configured or not: a scrubber's side
// output or an attacker-influenced value in the plan must never reach a
// comment as a command. Stripped warnings and errors are re-emitted through
// the CI's own logger, escaped; every other command line is dropped.
//
// Bypass is hard by construction:
//   - The only Filter is Configured(), which reads TFPR_FILTER itself, so no
//     caller can swap in an empty command.
//   - Text has no exported constructor. The only way to get one is
//     Filter.Apply, and the sinks (vcs.VCS.UpsertComment) accept only Text.
//
// This lives in its own package, not in core, for two reasons. Running a
// command is I/O, and core is pure. And Text must be unforgeable outside the
// package that applies the filter.
package redact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Redaction modes, as recorded in plan-result.json.
const (
	ModeNone   = "none"   // no filter ran: names from plan.json are unfiltered
	ModeFilter = "filter" // TFPR_FILTER ran over the plan text and the names
)

// ErrFailed wraps every filter failure.
var ErrFailed = errors.New("redaction filter failed")

// timeout bounds how long the filter may take to finish once its input has
// ended: the whole of one Apply, or the tail of a stream after the producer
// (say, a terraform apply that ran for hours) closed it. Tests shorten it.
var timeout = 2 * time.Minute

// waitDelay bounds the wait for the filter's output pipes after it exits or
// is killed, in case it left a child process holding them open. Tests
// shorten it.
var waitDelay = 5 * time.Second

// Filter is the configured redaction filter.
type Filter struct {
	command string
	diag    io.Writer               // the filter's own stderr, line-prefixed
	log     func(level, msg string) // re-emits stripped warnings and errors
}

// Configured returns the filter named by TFPR_FILTER (none when it is
// empty). The filter's stderr goes to diag with every line prefixed, so it
// cannot issue CI commands; log receives the warnings and errors stripped
// from filtered text (level "warning" or "error").
func Configured(diag io.Writer, log func(level, msg string)) Filter {
	return Filter{command: os.Getenv("TFPR_FILTER"), diag: &prefixer{w: diag, prefix: "tfpr filter: ", start: true}, log: log}
}

// Mode is ModeFilter when a filter is configured, else ModeNone.
func (f Filter) Mode() string {
	if f.command == "" {
		return ModeNone
	}
	return ModeFilter
}

// Text is text that has been through the configured filter.
type Text struct{ s string }

// String returns the redacted text.
func (t Text) String() string { return t.s }

// Apply runs the filter over s, then strips CI command lines.
func (f Filter) Apply(s string) (Text, error) {
	if f.command == "" {
		return Text{f.stripCommands(s)}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := f.cmd(ctx)
	cmd.Stdin, cmd.Stdout = strings.NewReader(s), &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s", timeout)
		}
		return Text{}, fmt.Errorf("%w: %v", ErrFailed, err)
	}
	if s != "" && out.Len() == 0 {
		return Text{}, fmt.Errorf("%w: empty output for non-empty input", ErrFailed)
	}
	return Text{f.stripCommands(out.String())}, nil
}

// stripCommands removes every line that starts, after optional whitespace,
// with a CI command prefix. Log-issue warnings and errors among them are
// re-emitted through f.log; the rest are counted in one warning.
func (f Filter) stripCommands(s string) string {
	var b strings.Builder
	dropped := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		cmd := strings.TrimLeft(line, " \t\v\f\r")
		if !strings.HasPrefix(cmd, "##vso[") && !strings.HasPrefix(cmd, "::") {
			b.WriteString(line)
			continue
		}
		if level, msg, ok := LogIssue(strings.TrimRight(cmd, "\r\n")); ok && f.log != nil {
			f.log(level, msg)
		} else {
			dropped++
		}
	}
	if dropped > 0 && f.log != nil {
		f.log("warning", fmt.Sprintf("Dropped %d CI command line(s) from filter output or plan text.", dropped))
	}
	return b.String()
}

// LogIssue parses a warning or error logging command, in Azure Pipelines
// (##vso[task.logissue type=warning;...]msg) or GitHub Actions
// (::warning ...::msg) syntax, into its level and message.
func LogIssue(line string) (level, msg string, ok bool) {
	if rest, found := strings.CutPrefix(line, "##vso["); found {
		head, msg, found := strings.Cut(rest, "]")
		name, props, _ := strings.Cut(head, " ")
		if !found || !strings.EqualFold(name, "task.logissue") {
			return "", "", false
		}
		for _, p := range strings.Split(props, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
			if v = strings.ToLower(v); strings.EqualFold(k, "type") && (v == "warning" || v == "error") {
				return v, msg, true
			}
		}
		return "", "", false
	}
	if rest, found := strings.CutPrefix(line, "::"); found {
		head, msg, found := strings.Cut(rest, "::")
		name, _, _ := strings.Cut(head, " ")
		if found && (name == "warning" || name == "error") {
			return name, msg, true
		}
	}
	return "", "", false
}

// Writer returns a writer that passes everything written to it through the
// filter into w. Close ends the input, gives the filter up to the timeout to
// finish (no limit applies while input is still flowing), and reports a
// filter failure. With no filter configured it writes straight to w. Writes
// never fail, so a producer streaming into it is never blocked; failures
// surface on Close.
func (f Filter) Writer(w io.Writer) io.WriteCloser {
	if f.command == "" {
		return nopCloser{w}
	}
	s := &stream{out: &counter{w: w}}
	s.cmd = f.cmd(context.Background())
	s.cmd.Stdout = s.out
	s.in, s.err = s.cmd.StdinPipe()
	if s.err == nil {
		s.err = s.cmd.Start()
	}
	return s
}

func (f Filter) cmd(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", f.command)
	cmd.WaitDelay = waitDelay
	cmd.Stderr = f.diag
	cmd.Env = WithoutTFPR(os.Environ())
	return cmd
}

// WithoutTFPR is env without tfpr's own TFPR_* variables (including the API
// token); child processes such as terraform and the filter get this.
func WithoutTFPR(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TFPR_") {
			out = append(out, kv)
		}
	}
	return out
}

type stream struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *counter
	n   int64 // bytes written in
	err error // first failure
}

func (s *stream) Write(p []byte) (int, error) {
	if s.err == nil {
		s.n += int64(len(p))
		_, s.err = s.in.Write(p)
	}
	return len(p), nil
}

func (s *stream) Close() error {
	if s.in != nil {
		s.in.Close()
	}
	if s.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- s.cmd.Wait() }()
		var err error
		select {
		case err = <-done:
		case <-time.After(timeout):
			_ = s.cmd.Process.Kill()
			<-done
			err = fmt.Errorf("did not finish within %s after its input ended", timeout)
		}
		if err != nil && s.err == nil {
			s.err = err
		}
	}
	if s.err == nil && s.n > 0 && s.out.n == 0 {
		s.err = errors.New("empty output for non-empty input")
	}
	if s.err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, s.err)
	}
	return nil
}

type counter struct {
	w io.Writer
	n int64
}

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.w.Write(p)
}

// prefixer starts every line written to w with prefix, so text from a child
// process can never begin a line with a CI command.
type prefixer struct {
	mu     sync.Mutex
	w      io.Writer
	prefix string
	start  bool // at the start of a line
}

func (p *prefixer) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []byte
	for _, c := range b {
		if p.start {
			out = append(out, p.prefix...)
		}
		out = append(out, c)
		p.start = c == '\n'
	}
	if _, err := p.w.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
