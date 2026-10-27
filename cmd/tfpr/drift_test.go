package main

import (
	"net/http/httptest"
	"testing"
)

// Drift mode touches no PR, even on a pull_request event: the full plan
// goes to the run summary, has_changes says whether anything drifted, and a
// warning names the stack.
func TestDriftGitHub(t *testing.T) {
	s := newScenario(t).set("TFPR_MODE", "drift", "GITHUB_EVENT_NAME", "pull_request")
	s.byo("changes")
	r := s.run("comment")
	eq(t, "exit code", 0, r.rc)
	lacks(t, "no PR touched", r.api, "/issues/")
	lacks(t, "no PR lookup", r.api, "/pulls")
	has(t, "summary: the full plan", r.summary, "Terraform plan")
	has(t, "has_changes", r.outputs, "has_changes=true")
	has(t, "warns", r.out, "::warning::Drift detected in 'stack' (default): 1 to add, 1 to change, 1 to destroy, 1 to replace, 0 to import.")

	r = s.set("TFPR_FAIL_ON_DRIFT", "true").run("comment")
	eq(t, "fail on drift: GitHub has no partial status, so it fails", 1, r.rc)
	has(t, "fail on drift: names the stack", r.out, "::error::failing on drift (TFPR_FAIL_ON_DRIFT=true): Drift detected in 'stack'")

	s = newScenario(t).set("TFPR_MODE", "drift", "TFPR_FAIL_ON_DRIFT", "true")
	s.byo("no-changes")
	r = s.run("comment")
	eq(t, "no drift: exit code", 0, r.rc)
	has(t, "no drift: has_changes", r.outputs, "has_changes=false")
	lacks(t, "no drift: no warning", r.out, "::warning::")

	r = newScenario(t).set("TFPR_MODE", "nope").run("comment")
	eq(t, "bad mode", 1, r.rc)
	has(t, "bad mode: says so", r.out, "TFPR_MODE must be pr or drift")
}

func TestDriftAzure(t *testing.T) {
	ado := &fakeAzureRepos{}
	srv := httptest.NewServer(ado)
	t.Cleanup(srv.Close)
	s := newScenario(t)
	s.azure("TfsGit").set("SYSTEM_COLLECTIONURI", srv.URL+"/org/", "TFPR_ADO_TOKEN", "t", "AGENT_TEMPDIRECTORY", t.TempDir(),
		"TFPR_MODE", "drift", "TFPR_FAIL_ON_DRIFT", "true")
	s.byo("output-only")
	r := s.run("comment")
	eq(t, "exit code: SucceededWithIssues is not a failure", 0, r.rc)
	has(t, "summary uploaded", r.out, "##vso[task.uploadsummary]")
	has(t, "has_changes", r.out, "##vso[task.setvariable variable=has_changes;isOutput=true]true\n")
	has(t, "warning", r.out, "##vso[task.logissue type=warning]Drift detected in 'stack' (default)")
	has(t, "tag", r.out, "##vso[build.addbuildtag]drift\n")
	has(t, "succeeded with issues", r.out, "##vso[task.complete result=SucceededWithIssues;]Drift detected in 'stack'")
	eq(t, "no PR touched", 0, len(ado.calls))
}
