package redact

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

func filter(t *testing.T, command string) Filter {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("filters run with sh")
	}
	t.Setenv("TFPR_FILTER", command)
	return Configured(io.Discard, nil)
}

// logged collects re-emitted log issues.
type logged []string

func (l *logged) log(level, msg string) { *l = append(*l, level+": "+msg) }

func TestApplyStripsCommandLines(t *testing.T) {
	for _, command := range []string{"", "cat"} {
		var l logged
		t.Setenv("TFPR_FILTER", command)
		f := Configured(io.Discard, l.log)
		in := "keep 1\n  ##vso[task.setvariable variable=x]y\n::set-output name=a::b\n" +
			"##vso[task.logissue type=warning;sourcepath=x]masked 3 values\n\t::error file=a::boom 100%\nkeep ##vso[ mid-line\n"
		got, err := f.Apply(in)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != "keep 1\nkeep ##vso[ mid-line\n" {
			t.Errorf("filter %q: kept %q", command, got)
		}
		want := logged{"warning: masked 3 values", "error: boom 100%", "warning: Dropped 2 CI command line(s) from filter output or plan text."}
		if strings.Join(l, "|") != strings.Join(want, "|") {
			t.Errorf("filter %q: logged %q", command, l)
		}
	}
}

// A scrubber that reports on stdout: its report is re-emitted, not posted.
func TestFilterSideOutput(t *testing.T) {
	var l logged
	t.Setenv("TFPR_FILTER", `sed s/hunter2/X/; echo '##vso[task.logissue type=warning]masked 1 value'`)
	got, err := Configured(io.Discard, l.log).Apply("pw hunter2\n")
	if err != nil || got.String() != "pw X\n" || len(l) != 1 || l[0] != "warning: masked 1 value" {
		t.Errorf("got %q, %v, logged %q", got, err, l)
	}
}

func TestLogIssue(t *testing.T) {
	for line, want := range map[string]string{
		"##vso[task.logissue type=error]x":          "error|x",
		"##vso[task.LogIssue type=Warning;code=1]y": "warning|y",
		"##vso[task.logissue type=info]x":           "",
		"##vso[task.setvariable variable=a]b":       "",
		"::warning::m":                              "warning|m",
		"::error title=t,file=f::m::n":              "error|m::n",
		"::notice::m":                               "",
		"::stop-commands::tok":                      "",
	} {
		level, msg, ok := LogIssue(line)
		got := ""
		if ok {
			got = level + "|" + msg
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
}

func TestFilterStderrIsPrefixed(t *testing.T) {
	var diag bytes.Buffer
	t.Setenv("TFPR_FILTER", `cat; echo '::warning::from stderr' >&2; echo second >&2`)
	if _, err := Configured(&diag, nil).Apply("x"); err != nil {
		t.Fatal(err)
	}
	if diag.String() != "tfpr filter: ::warning::from stderr\ntfpr filter: second\n" {
		t.Errorf("diag %q", diag.String())
	}
}

func TestNoneConfiguredIsIdentity(t *testing.T) {
	f := filter(t, "")
	if f.Mode() != ModeNone {
		t.Errorf("mode %q", f.Mode())
	}
	got, err := f.Apply("secret")
	if err != nil || got.String() != "secret" {
		t.Errorf("got %q, %v", got, err)
	}
	var b bytes.Buffer
	w := f.Writer(&b)
	io.WriteString(w, "secret")
	if err := w.Close(); err != nil || b.String() != "secret" {
		t.Errorf("writer: %q, %v", b.String(), err)
	}
}

func TestApplyTransforms(t *testing.T) {
	f := filter(t, "sed s/hunter2/REDACTED/g")
	if f.Mode() != ModeFilter {
		t.Errorf("mode %q", f.Mode())
	}
	got, err := f.Apply("password = hunter2\n")
	if err != nil || got.String() != "password = REDACTED\n" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestApplyFailsClosed(t *testing.T) {
	for name, command := range map[string]string{
		"non-zero exit": "cat; exit 3",
		"empty output":  "cat > /dev/null",
		"not found":     "/nonexistent/filter",
	} {
		got, err := filter(t, command).Apply("text")
		if !errors.Is(err, ErrFailed) || got.String() != "" {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
	// Empty input may legitimately produce empty output.
	if _, err := filter(t, "cat").Apply(""); err != nil {
		t.Errorf("empty input: %v", err)
	}
}

func TestApplyTimesOut(t *testing.T) {
	defer func(d time.Duration) { timeout = d }(timeout)
	timeout = 100 * time.Millisecond
	if _, err := filter(t, "sleep 5").Apply("x"); !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("got %v", err)
	}
}

func TestWriterStreams(t *testing.T) {
	var b bytes.Buffer
	w := filter(t, "sed s/hunter2/REDACTED/g").Writer(&b)
	for _, line := range []string{"a hunter2\n", "b hunter2\n"} {
		io.WriteString(w, line)
	}
	if err := w.Close(); err != nil || b.String() != "a REDACTED\nb REDACTED\n" {
		t.Errorf("got %q, %v", b.String(), err)
	}
}

func TestWriterFailsClosed(t *testing.T) {
	for name, command := range map[string]string{
		"non-zero exit": "cat > /dev/null; exit 1",
		"empty output":  "cat > /dev/null",
		"exits early":   "exit 0",
	} {
		var b bytes.Buffer
		w := filter(t, command).Writer(&b)
		for i := 0; i < 1000; i++ { // more than a pipe buffer: writes must not block
			io.WriteString(w, strings.Repeat("x", 100)+"\n")
		}
		if err := w.Close(); !errors.Is(err, ErrFailed) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestFilterEnvHasNoTFPRVariables(t *testing.T) {
	f := filter(t, "env")
	t.Setenv("TFPR_GITHUB_TOKEN", "tok")
	got, err := f.Apply("x")
	if err != nil || strings.Contains(got.String(), "TFPR_") {
		t.Errorf("filter saw tfpr's variables: %v\n%s", err, got)
	}
}
