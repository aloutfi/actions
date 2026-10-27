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

// recordLine is a plan record for stack/default as the PR's plan wrote it.
func recordLine(status, tree string) string {
	return core.Record(core.NewPlanResult(core.PlanResult{
		Target: core.Target{Stack: "stack", Workspace: "default"},
		Source: core.Source{SHA: "prmerge", Tree: tree},
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

	s.set("TFPR_APPLY_EXIT_CODE", "")
	r = s.run("report")
	eq(t, "no exit code: fails", 1, r.rc)
	has(t, "no exit code: says so", r.out, "TFPR_APPLY_EXIT_CODE must be the exit code of your terraform apply")

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
