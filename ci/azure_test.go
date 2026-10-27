package ci

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloutfi/actions/redact"
)

// snapshot reads testdata/env/<name>.env: KEY=VALUE lines, # comments.
func snapshot(t *testing.T, name string) Env {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "env", name+".env"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && !strings.HasPrefix(k, "#") {
			m[k] = v
		}
	}
	return envOf(m)
}

func TestDetectSnapshots(t *testing.T) {
	for name, want := range map[string]string{
		"azure-pr-tfsgit":     "azure",
		"azure-pr-github":     "azure",
		"azure-main-tfsgit":   "azure",
		"github-pr":           "github",
		"github-and-tf-build": "github",
		"local":               "local",
	} {
		got := Detect(snapshot(t, name), io.Discard, io.Discard, nil).Context().CI
		if got != want {
			t.Errorf("%s: detected %s, want %s", name, got, want)
		}
	}
}

func TestAzureContext(t *testing.T) {
	for name, want := range map[string]Ctx{
		"azure-pr-tfsgit": {
			CI: "azure", RunID: "812",
			RunURL:  "https://dev.azure.example/org/Platform%20Team/_build/results?buildId=812",
			SHA:     "feedfeedfeedfeedfeedfeedfeedfeedfeedfeed", // the merge commit checked out
			HeadSHA: "abcabcabcabcabcabcabcabcabcabcabcabcabca", // the PR head
			Ref:     "refs/pull/7/merge", PR: "7", Actor: "Example Person", Attempt: 1,
			Repo: "99999999-8888-7777-6666-555555555555/11111111-2222-3333-4444-555555555555",
			VCS:  "azure", VCSURL: "https://dev.azure.example/org/",
		},
		"azure-pr-github": {
			CI: "azure", RunID: "813",
			RunURL:  "https://dev.azure.example/org/proj/_build/results?buildId=813",
			SHA:     "feedfeedfeedfeedfeedfeedfeedfeedfeedfeed",
			HeadSHA: "abcabcabcabcabcabcabcabcabcabcabcabcabca",
			Ref:     "refs/pull/42/merge", PR: "42", Attempt: 2, // GitHub's number, not Azure's id
			Repo: "o/r", VCS: "github",
		},
		"azure-main-tfsgit": {
			CI: "azure", RunID: "814",
			RunURL: "https://dev.azure.example/org/proj/_build/results?buildId=814",
			SHA:    "feedfeedfeedfeedfeedfeedfeedfeedfeedfeed", Ref: "refs/heads/main", Attempt: 1,
			Repo: "99999999-8888-7777-6666-555555555555/11111111-2222-3333-4444-555555555555",
			VCS:  "azure", VCSURL: "https://dev.azure.example/org/",
		},
	} {
		if got := newAzure(snapshot(t, name), io.Discard, io.Discard, nil).Context(); got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
	if got := newAzure(envOf(map[string]string{"TF_BUILD": "True"}), io.Discard, io.Discard, nil).Context().Attempt; got != 0 {
		t.Errorf("no System.JobAttempt: attempt %d, want 0 (unknown)", got)
	}
}

// GitHub Enterprise: the API is on the repository's host, and without that
// host the context says so instead of leaving the public API as a default.
func TestAzureGitHubEnterprise(t *testing.T) {
	c := newAzure(snapshot(t, "azure-pr-ghe"), io.Discard, io.Discard, nil).Context()
	if c.VCS != "github" || c.VCSURL != "https://ghe.example.com/api/v3" || !c.VCSPrivate || c.Repo != "team/infra" || c.PR != "3" {
		t.Errorf("context %+v", c)
	}
	c = newAzure(envOf(map[string]string{"BUILD_REPOSITORY_PROVIDER": "GitHubEnterprise"}), io.Discard, io.Discard, nil).Context()
	if c.VCSURL != "" || !c.VCSPrivate {
		t.Errorf("no repository URI: %+v", c)
	}
	c = newAzure(snapshot(t, "azure-pr-github"), io.Discard, io.Discard, nil).Context()
	if c.VCSPrivate || c.VCSURL != "" {
		t.Errorf("github.com: %+v", c)
	}
}

func TestAzureLogsBothPRCommits(t *testing.T) {
	var stderr bytes.Buffer
	newAzure(snapshot(t, "azure-pr-tfsgit"), io.Discard, &stderr, nil)
	if !strings.Contains(stderr.String(), "SourceCommitId=abcabc") || !strings.Contains(stderr.String(), "Build.SourceVersion=feedfeed") {
		t.Errorf("stderr %q", stderr.String())
	}
}

func TestAzureJobURL(t *testing.T) {
	a := newAzure(snapshot(t, "azure-pr-tfsgit"), io.Discard, io.Discard, nil)
	if got := a.JobURL(); got != "https://dev.azure.example/org/Platform%20Team/_build/results?buildId=812&view=logs&j=aaaa-bbbb" {
		t.Errorf("got %s", got)
	}
}

func TestAzureCommands(t *testing.T) {
	var out bytes.Buffer
	a := newAzure(envOf(nil), &out, io.Discard, nil).(*Azure)
	_ = a.SetOutput("summary", "1 to add; 100% ]done\r\nnext")
	_ = a.SetOutput("we;ird]name", "x")
	a.Log(Warning, "careful ##vso[task.setvariable variable=x]y")
	a.Log(Error, "50%\nbad")
	a.Log(Notice, "##[error]not really")
	a.Log(Notice, "100% plain\nline two ##vso[x]")
	_ = a.AddBuildTag("drift")
	_ = a.CompleteWithIssues("drift found")
	want := strings.Join([]string{
		"##vso[task.setvariable variable=summary;isOutput=true]1 to add; 100%AZP25 ]done%0D%0Anext",
		"##vso[task.setvariable variable=we%3Bird%5Dname;isOutput=true]x",
		"##vso[task.logissue type=warning]careful # #vso[task.setvariable variable=x]y",
		"##vso[task.logissue type=error]50%AZP25%0Abad",
		"# #[error]not really",
		"100% plain",
		"line two # #vso[x]",
		"##vso[build.addbuildtag]drift",
		"##vso[task.complete result=SucceededWithIssues;]drift found",
	}, "\n") + "\n"
	if out.String() != want {
		t.Errorf("got\n%s\nwant\n%s", out.String(), want)
	}
}

func TestAzureUploadSummary(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	a := newAzure(envOf(map[string]string{"AGENT_TEMPDIRECTORY": dir}), &out, io.Discard, nil).(*Azure)
	t.Setenv("TFPR_FILTER", "")
	md, err := redact.Configured(io.Discard, nil).Apply("## Plan\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UploadSummary(md); err != nil {
		t.Fatal(err)
	}
	path, ok := strings.CutPrefix(strings.TrimSpace(out.String()), "##vso[task.uploadsummary]")
	if !ok || filepath.Dir(path) != dir {
		t.Fatalf("command %q", out.String())
	}
	if data, _ := os.ReadFile(path); string(data) != "## Plan\n" {
		t.Errorf("summary file %q", data)
	}
}

// Echoed text can't issue commands: every ##vso[ and ##[ is broken up, even
// mid-line and across writes.
func TestAzureInert(t *testing.T) {
	var out bytes.Buffer
	w := newAzure(envOf(nil), &out, io.Discard, nil).Inert(&out)
	for _, chunk := range []string{"plan line\n  ##vso[task.setvariable variable=x]y\nvalue = \"a##v", "so[task.complete]\"\n##[error]x\ntail ##vso[no newline"} {
		io.WriteString(w, chunk)
	}
	w.Close()
	want := "plan line\n  # #vso[task.setvariable variable=x]y\nvalue = \"a# #vso[task.complete]\"\n# #[error]x\ntail # #vso[no newline"
	if out.String() != want {
		t.Errorf("got  %q\nwant %q", out.String(), want)
	}
	if strings.Contains(out.String(), "##vso[") || strings.Contains(out.String(), "##[") {
		t.Error("a command survived")
	}
}
