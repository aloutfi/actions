package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aloutfi/actions/core"
)

// gitCommit makes the workspace a git repo with one commit and returns its
// id and tree, so the gate can compare the merged tree with a record's.
func (s *scenario) gitCommit() (sha, tree string) {
	s.t.Helper()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = s.ws
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			s.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	s.write("stack/main.tf", "# stack\n")
	git("add", ".")
	git("commit", "-q", "-m", "merge")
	return git("rev-parse", "HEAD"), git("rev-parse", "HEAD^{tree}")
}

// recordLine is a plan record for stack/default as the PR's plan wrote it,
// planned for the fake PR's head ("headsha").
func recordLine(status, tree string) string { return recordFor(status, tree, "headsha") }

func recordFor(status, tree, head string) string {
	return core.Record(core.NewPlanResult(core.PlanResult{
		Target: core.Target{Stack: "stack", Workspace: "default"},
		Source: core.Source{SHA: "prmerge", Tree: tree, HeadSHA: head},
		Run:    core.Run{ID: "77", Attempt: 1},
	}, []core.Unit{{Path: ".", Status: status, Skippable: status == core.StatusNoOp}}))
}

// planComment is the PR's plan comment by author, as the GitHub API lists it.
func planComment(id int, author, record string) string {
	body, _ := json.Marshal("<!-- tf-plan:stack :: default -->\n" + record + "\n### plan")
	return fmt.Sprintf(`{"id":%d,"user":{"login":%q},"body":%s}`, id, author, body)
}

func (s *scenario) decision() core.GateResult {
	s.t.Helper()
	var g core.GateResult
	data, err := os.ReadFile(filepath.Join(s.ws, "stack", ".tfpr", "decision.json"))
	must(s.t, err)
	must(s.t, json.Unmarshal(data, &g))
	return g
}

func TestGate(t *testing.T) {
	s := newScenario(t)
	s.gh.comments = "[" + planComment(5, "github-actions[bot]", recordLine(core.StatusChanges, "T")) + "]"
	r := s.run("gate")
	eq(t, "exit code", 0, r.rc)
	eq(t, "outputs", "apply=true\naction=apply\nreason=\npr_number=42\nplan_run_id=77\ndownload=false\nartifact_name=tfplan-stack-default\n", r.outputs)
	g := s.decision()
	if g.Action != core.GateApply || g.PR != "42" || g.PlanRun != "77" || g.SHA != "abc" || g.BranchHead != "abc" {
		t.Errorf("decision.json: %+v", g)
	}

	// With the planfile flow it empties the download dir and asks for it.
	s = newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.planArtifact("stack", "default")
	r = s.run("gate")
	has(t, "planfile: download", r.outputs, "download=true")
	_, err := os.Stat(filepath.Join(s.ws, "artifact"))
	eq(t, "planfile: empties the artifact dir", true, os.IsNotExist(err))

	// Superseded: a clean skip.
	s = newScenario(t)
	s.gh.branchHead = "def"
	r = s.run("gate")
	eq(t, "superseded: exit code", 0, r.rc)
	has(t, "superseded: outputs", r.outputs, "apply=false\naction=skip\nreason=superseded\n")
	has(t, "superseded: warns", r.out, "::warning::Skipping the apply")
	eq(t, "superseded: decision", "def", s.decision().BranchHead)

	// Unverifiable: refused.
	r = newScenario(t).set("GITHUB_REF", "refs/tags/v1", "GITHUB_RUN_ATTEMPT", "2").run("gate")
	eq(t, "unverified: exit code", 1, r.rc)
	has(t, "unverified: outputs", r.outputs, "apply=false\naction=refuse\nreason=unverified-ref\n")
}

// Skip on no-op: every condition holds, so the apply is skipped; any doubt
// applies.
func TestGateSkipNoop(t *testing.T) {
	setup := func(author, status string, treeOf func(tree string) string) (*scenario, string) {
		s := newScenario(t).set("TFPR_SKIP_NOOP", "true")
		sha, tree := s.gitCommit()
		s.set("GITHUB_SHA", sha)
		s.gh.branchHead = sha
		s.gh.comments = "[" + planComment(5, author, recordLine(status, treeOf(tree))) + "]"
		return s, sha
	}
	same := func(tree string) string { return tree }

	s, _ := setup("github-actions[bot]", core.StatusNoOp, same)
	r := s.run("gate")
	eq(t, "no-op: exit code", 0, r.rc)
	has(t, "no-op: skipped", r.outputs, "apply=false\naction=skip\nreason=no-op\n")
	has(t, "no-op: says why", r.out, "::notice::Skipping the apply of 'stack'")

	// A record someone else wrote, even a perfect one, is ignored.
	s, _ = setup("someone", core.StatusNoOp, same)
	r = s.run("gate")
	has(t, "forged record: applies", r.outputs, "apply=true\naction=apply\nreason=no-record\n")

	s, _ = setup("github-actions[bot]", core.StatusNoOp, func(string) string { return "another-tree" })
	has(t, "tree differs: applies", s.run("gate").outputs, "reason=tree-differs")

	s, _ = setup("github-actions[bot]", core.StatusChanges, same)
	has(t, "plan had changes: applies", s.run("gate").outputs, "reason=plan-has-changes")

	s, _ = setup("github-actions[bot]", core.StatusNoOp, same)
	s.set("GITHUB_RUN_ATTEMPT", "2")
	has(t, "re-run: applies", s.run("gate").outputs, "reason=rerun-or-unknown-attempt")

	s, _ = setup("github-actions[bot]", core.StatusNoOp, same)
	s.gh.pr = ""
	has(t, "no PR: applies", s.run("gate").outputs, "reason=no-pr")

	// tfpr apply honours the same decision: nothing to apply, nothing run.
	s, _ = setup("github-actions[bot]", core.StatusNoOp, same)
	r = s.run("apply")
	eq(t, "apply: exit code", 0, r.rc)
	lacks(t, "apply: no terraform apply", r.tf, "terraform apply")
	has(t, "apply: reports the skip", r.body, "skipped: no changes")
}

// report: the caller ran terraform apply itself.
func TestReport(t *testing.T) {
	s := newScenario(t)
	eq(t, "gate", 0, s.run("gate").rc)
	s.write("apply.log", "terraform_data.x: Creating... secret-value\nApply complete! Resources: 1 added, 0 changed, 0 destroyed.\n::set-output name=x::y\n")
	s.set("TFPR_APPLY_EXIT_CODE", "0", "TFPR_APPLY_LOG", "apply.log", "TFPR_FILTER", "sed s/secret-value/REDACTED/g")
	r := s.run("report")
	eq(t, "exit code", 0, r.rc)
	has(t, "posts on the merged PR", r.api, "POST /repos/o/r/issues/42/comments")
	has(t, "succeeded", r.body, "### ✅ Terraform apply · `stack` (`default`) · succeeded")
	has(t, "the log, filtered", r.body, "Creating... REDACTED")
	lacks(t, "the log, filtered", r.body, "secret-value")
	lacks(t, "no command lines", r.body, "::set-output")
	has(t, "marker", r.body, "<!-- tf-apply:stack :: default -->\n")

	// The apply step never ran its script: no exit code, no log. The PR still
	// hears about it, and report fails after posting.
	s.set("TFPR_APPLY_EXIT_CODE", "", "TFPR_APPLY_LOG", "missing.log")
	r = s.run("report")
	eq(t, "no exit code: fails", 1, r.rc)
	has(t, "no exit code: posts", r.body, "did not complete")
	has(t, "no exit code: why", r.body, "> **Apply did not complete** (no exit code; the apply step may have failed before it started or been cancelled), see [the run](")
	has(t, "no exit code: missing log is a warning", r.out, "::warning::TFPR_APPLY_LOG missing.log does not exist")
	has(t, "no exit code: error after posting", r.out, "::error::the apply did not complete: TFPR_APPLY_EXIT_CODE is ''")

	// A superseded run reports without an exit code, linking the newer run.
	s = newScenario(t)
	s.gh.branchHead, s.gh.headRun = "def", "https://github.example/o/r/actions/runs/9"
	s.run("gate")
	r = s.run("report")
	eq(t, "superseded: exit code", 0, r.rc)
	has(t, "superseded: message", r.body, "> **Superseded by `def`**; that commit's run applies these changes ([link](https://github.example/o/r/actions/runs/9)).")

	// No PR merged this commit: nothing to post.
	s = newScenario(t).set("TFPR_APPLY_EXIT_CODE", "0")
	s.gh.pr = ""
	r = s.run("report")
	eq(t, "no PR: exit code", 0, r.rc)
	has(t, "no PR: says so", r.out, "No PR associated with abc; skipping apply-result comment.")
}

// Gate and report on Azure Pipelines with Azure Repos.
func TestAzureGateAndReport(t *testing.T) {
	ado := &fakeAzureRepos{mergedPR: 7, head: "newer",
		threads: `[{"id":5,"status":"closed","properties":{
			"tfpr.marker":{"$type":"System.String","$value":"<!-- tf-plan:stack :: default -->"},
			"tfpr.record":{"$type":"System.String","$value":` + jsonString(recordLine(core.StatusChanges, "T")) + `}},
			"comments":[{"id":1,"author":{"id":"build-id"}}]}]`,
		builds: `[{"sourceVersion":"newer","_links":{"web":{"href":"https://dev.azure.example/org/proj/_build/results?buildId=99"}}}]`}
	srv := httptest.NewServer(ado)
	t.Cleanup(srv.Close)
	s := newScenario(t)
	s.azure("TfsGit").set("SYSTEM_COLLECTIONURI", srv.URL+"/org/", "TFPR_ADO_TOKEN", "t", "SYSTEM_DEFINITIONID", "12",
		"BUILD_SOURCEBRANCH", "refs/heads/main", "BUILD_SOURCEVERSION", "squash1",
		"SYSTEM_PULLREQUEST_SOURCECOMMITID", "", "SYSTEM_PULLREQUEST_PULLREQUESTID", "")
	r := s.run("gate")
	eq(t, "superseded: exit code", 0, r.rc)
	has(t, "superseded: output variable", r.out, "##vso[task.setvariable variable=apply;isOutput=true]false\n")
	has(t, "superseded: warning", r.out, "##vso[task.logissue type=warning]Skipping the apply")

	r = s.run("report")
	eq(t, "report: exit code", 0, r.rc)
	content := ado.created["comments"].([]any)[0].(map[string]any)["content"].(string)
	has(t, "report: superseded, linking the newer build", content, "([link](https://dev.azure.example/org/proj/_build/results?buildId=99))")
	eq(t, "report: a skip is resolved", any("closed"), ado.created["status"])
	lacks(t, "report: Azure dialect", content, "<details")

	// A newer head that changed only other stacks: apply.
	s.gitCommit()
	ado.diff = `{"allChangesIncluded":true,"changes":[{"item":{"path":"/other"}},{"item":{"path":"/other/main.tf"}}]}`
	r = s.run("gate")
	eq(t, "stack unchanged: exit code", 0, r.rc)
	has(t, "stack unchanged: output variable", r.out, "##vso[task.setvariable variable=apply;isOutput=true]true\n")
	ado.diff = ""

	// At the head: apply, with the plan run from the thread's record.
	ado.head = "squash1"
	r = s.run("gate")
	eq(t, "head: exit code", 0, r.rc)
	has(t, "head: apply", r.out, "##vso[task.setvariable variable=apply;isOutput=true]true\n")
	has(t, "head: PR", r.out, "##vso[task.setvariable variable=pr_number;isOutput=true]7\n")
	has(t, "head: plan run", r.out, "##vso[task.setvariable variable=plan_run_id;isOutput=true]77\n")
	r = s.set("TFPR_APPLY_EXIT_CODE", "2").run("report")
	content = ado.created["comments"].([]any)[0].(map[string]any)["content"].(string)
	has(t, "failed apply", content, "failed (exit 2)")
	eq(t, "a failed apply stays active", any("active"), ado.created["status"])
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The PR's last plan record is for an older push (a newer push's plan was
// cancelled or is still running): the gate hands out no plan run, and an
// apply that wants the reviewed planfile fails closed instead of applying a
// plan nobody reviewed for this merge.
func TestGateIgnoresAStalePlan(t *testing.T) {
	stale := "[" + planComment(5, "github-actions[bot]", recordFor(core.StatusChanges, "T", "push1")) + "]"
	s := newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.gh.comments = stale // the PR's head is "headsha" (push 2)
	r := s.run("gate")
	eq(t, "gate: exit code", 0, r.rc)
	has(t, "gate: no plan run, no download", r.outputs, "plan_run_id=\ndownload=false\n")
	has(t, "gate: says why", r.out, "::warning::No reviewed planfile for PR #42's head headsha: record-not-for-pr-head.")

	// Even if push 1's planfile were downloaded, apply refuses it.
	s.planArtifactFor("stack", "default", "prmerge", "push1")
	r = s.run("apply")
	eq(t, "apply: fails closed", 1, r.rc)
	lacks(t, "apply: no terraform apply", r.tf, "terraform apply")
	has(t, "apply: missing artifact", r.outputs, "fallback_reason=missing-artifact")

	// A trusted record, but an artifact planned at another commit: refused too.
	s = newScenario(t).set("TFPR_USE_PLANFILE", "true", "TFPR_PLAN_DIR", "artifact")
	s.planArtifact("stack", "default")
	s.planArtifactFor("stack", "default", "othermerge", "headsha")
	r = s.run("apply")
	eq(t, "other commit: fails closed", 1, r.rc)
	has(t, "other commit: says why", r.out, "Ignoring the planfile artifact: it was not planned for the merged PR's head")

	// Skip on no-op won't trust it either.
	s = newScenario(t).set("TFPR_SKIP_NOOP", "true")
	s.gh.comments = "[" + planComment(5, "github-actions[bot]", recordFor(core.StatusNoOp, "T", "push1")) + "]"
	has(t, "skip-noop: applies", s.run("gate").outputs, "reason=record-not-for-pr-head")
}

// On Azure Pipelines, the AzureCLI task's login fails before its script runs:
// the exit-code variable is never set and there is no apply log. report
// still posts "did not complete" and leaves the thread active.
func TestAzureReportAfterLoginFailure(t *testing.T) {
	ado := &fakeAzureRepos{mergedPR: 7}
	srv := httptest.NewServer(ado)
	t.Cleanup(srv.Close)
	s := newScenario(t)
	s.azure("TfsGit").set("SYSTEM_COLLECTIONURI", srv.URL+"/org/", "TFPR_ADO_TOKEN", "t", "SYSTEM_DEFINITIONID", "12",
		"BUILD_SOURCEBRANCH", "refs/heads/main", "BUILD_SOURCEVERSION", "squash1", "SYSTEM_JOBID", "job1",
		"SYSTEM_PULLREQUEST_SOURCECOMMITID", "", "SYSTEM_PULLREQUEST_PULLREQUESTID", "")
	has(t, "gate: apply", s.run("gate").out, "##vso[task.setvariable variable=apply;isOutput=true]true\n")
	r := s.set("TFPR_APPLY_LOG", filepath.Join(t.TempDir(), "apply.log")).run("report")
	eq(t, "exit code", 1, r.rc)
	has(t, "warning: no log", r.out, "##vso[task.logissue type=warning]TFPR_APPLY_LOG")
	content := ado.created["comments"].([]any)[0].(map[string]any)["content"].(string)
	has(t, "did not complete", content, "Apply did not complete")
	has(t, "links the run", content, "_build/results?buildId=812&view=logs&j=job1")
	eq(t, "thread stays active", any("active"), ado.created["status"])
}

// report posts where the gate found exactly one PR, and asks the repo host
// only once. With several PRs it posts nowhere and says which matched.
func TestReportUsesTheGatesPR(t *testing.T) {
	s := newScenario(t).set("TFPR_APPLY_EXIT_CODE", "0")
	s.gh.pulls = `[{"number":42,"head":{"sha":"a"}},{"number":43,"head":{"sha":"b"}}]`
	s.run("gate")
	r := s.run("report")
	eq(t, "exit code", 0, r.rc)
	has(t, "says which matched", r.out, "::warning::Several PRs match abc (#42, #43); not posting the apply result")
	lacks(t, "posts nothing", r.api, "POST")
	eq(t, "one lookup", 1, strings.Count(r.api, "/commits/abc/pulls"))

	s = newScenario(t).set("TFPR_APPLY_EXIT_CODE", "0")
	s.run("gate")
	r = s.run("report")
	has(t, "one PR: posts", r.api, "POST /repos/o/r/issues/42/comments")
	eq(t, "one PR: one lookup", 1, strings.Count(r.api, "/commits/abc/pulls"))
}

// The apply result honours TFPR_COMMENT_BUDGET after the filter: a filter
// that grows the text trims the log further, and in the worst case the
// result is posted without it.
func TestApplyCommentBudget(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&log, "terraform_data.item[%d]: Creating... a a a a a a\n", i)
	}
	log.WriteString("Apply complete! Resources: 500 added, 0 changed, 0 destroyed.\n")
	for name, filter := range map[string]string{
		"filter grows the text": "sed 's/a/aaaa/g'",
		"filter grows it a lot": "sed 's/./&&&&&&&&&&/g'",
	} {
		s := newScenario(t).set("TFPR_APPLY_EXIT_CODE", "0", "TFPR_APPLY_LOG", "apply.log", "TFPR_COMMENT_BUDGET", "3000", "TFPR_FILTER", filter)
		s.write("apply.log", log.String())
		s.run("gate")
		r := s.run("report")
		eq(t, name+": exit code", 0, r.rc)
		if n := utf8.RuneCountInString(r.body); n > 3000 {
			t.Errorf("%s: %d characters, over the 3,000 budget", name, n)
		}
		has(t, name+": posted", r.api, "POST /repos/o/r/issues/42/comments")
		if name == "filter grows the text" {
			has(t, name+": keeps the log's end", r.body, "Apply complete! Resources: 500 ")
		} else {
			lacks(t, name+": minimal body without the log", r.body, "Apply output")
		}
	}
}
