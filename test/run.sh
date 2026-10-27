#!/usr/bin/env bash
# Black-box tests for the tfpr CLI: golden files for `tfpr render`, the pure
# helper case tables in test/cases/, and commands against a fake GitHub.
# Usage: [TFPR_BIN=path/to/tfpr] test/run.sh [--update]   (--update rewrites goldens)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tfpr="${TFPR_BIN:-${root}/bin/tfpr}"
[[ "${tfpr}" == /* ]] || tfpr="${PWD}/${tfpr}"
update=false
[[ "${1:-}" == "--update" ]] && update=true
failures=0
passes=0

pass() { passes=$((passes + 1)); }
fail() {
  failures=$((failures + 1))
  echo "FAIL: $*"
}

# assert_eq NAME EXPECTED ACTUAL
assert_eq() {
  if [[ "$2" == "$3" ]]; then pass; else fail "$1: expected '$2', got '$3'"; fi
}

# --- render golden files ----------------------------------------------------
cd "${root}/test/fixtures/render"
for dir in */; do
  name="${dir%/}"
  actual="$(mktemp)"
  budget=45000
  [[ -f "${name}/budget" ]] && budget="$(cat "${name}/budget")"
  TFPR_STACK="${name}" TFPR_WORKSPACE=default \
    TFPR_EXIT_CODE="$(cat "${name}/exit_code")" \
    TFPR_RUN_ID=1234 TFPR_JOB_URL="https://ci.example/run/1234/job/5678" \
    TFPR_COMMENT_BUDGET="${budget}" \
    "${tfpr}" render > "${actual}"
  if [[ "${update}" == "true" ]]; then
    cp "${actual}" "${name}/expected.md"
  fi
  if diff -u "${name}/expected.md" "${actual}"; then pass; else fail "render ${name}"; fi
  rm -f "${actual}"
done

# The over-budget case must actually be over budget and carry the notice.
if [[ "$(wc -c < truncated/plan.txt)" -gt "$(cat truncated/budget)" ]]; then pass; else
  fail "truncated fixture is not over its budget"; fi
if grep -q '^\[truncated' truncated/expected.md; then pass; else fail "truncation notice missing"; fi
cd "${root}"

# --- pure helpers -----------------------------------------------------------
# Case tables shared with the Go tests: tab-separated, '#' lines are headers.
# cases FILE: print the table's rows with tabs turned into \x1f, which `read`
# splits on without collapsing empty fields.
cases() { grep -v '^#' "${root}/test/cases/$1" | tr '\t' '\037'; }

# The bash core exposes its helpers by being sourced.
# shellcheck source=/dev/null
source "${tfpr}"

while IFS=$'\037' read -r name use attempt has allow expected; do
  assert_eq "decide_plan_source: ${name}" "${expected}" "$(decide_plan_source "${use}" "${attempt}" "${has}" "${allow}")"
done < <(cases decide_plan_source.tsv)

while IFS=$'\037' read -r fixture expected; do
  assert_eq "plan_stats: ${fixture}" "${expected//$'\037'/|}" \
    "$(plan_stats "${root}/test/fixtures/render/${fixture}/plan.json" | tr '\t' '|')"
done < <(cases plan_stats.tsv)

while IFS=$'\037' read -r name stack workspace expected; do
  assert_eq "artifact_name: ${name}" "${expected}" "$(artifact_name "${stack}" "${workspace}")"
done < <(cases artifact_name.tsv)

# --- commands against a fake GitHub ----------------------------------------
# A fake gh and a stub terraform on PATH; GitHub Actions env simulated per call.
sandbox="$(mktemp -d)"
trap 'rm -rf "${sandbox}"' EXIT
mkdir -p "${sandbox}/bin"
cat > "${sandbox}/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Fake gh: logs each call; FAKE_* variables shape the responses.
echo "gh $*" >> "${FAKE_LOG}"
case "$*" in
  *"-X PATCH"* | *"-X POST"*)
    if [[ -n "${FAKE_GH_FAIL_WRITE:-}" ]]; then echo "HTTP 403: forbidden" >&2; exit 1; fi
    # Keep the posted body for the golden comparisons.
    for arg in "$@"; do [[ "${arg}" != body=@* ]] || cp "${arg#body=@}" "${FAKE_BODY}"; done ;;
  *"/actions/runs/"*"/jobs"*) echo '{"jobs":[]}' ;;
  *"/issues/"*"/comments"*) echo '[]' ;;
  *"/commits/"*"/pulls"*) [[ -z "${FAKE_PR:-}" ]] || echo "${FAKE_PR} headsha" ;;
  *"/git/ref/heads/"*) echo "${FAKE_BRANCH_HEAD}" ;;
  *"/actions/workflows/"*"/runs?"*) echo "${FAKE_PLAN_RUN:-}" ;;
  *) echo "fake gh: unexpected call: $*" >&2; exit 1 ;;
esac
EOF
cat > "${sandbox}/bin/terraform" <<'EOF'
#!/usr/bin/env bash
# Stub terraform: logs each call; plan and apply exit with FAKE_TF_PLAN_CODE
# and FAKE_TF_APPLY_CODE; show prints the plan from FAKE_TF_PLAN (a fixture dir).
echo "terraform $*" >> "${FAKE_LOG}"
case "$1" in
  version) echo '{"terraform_version":"0.0.0"}' ;;
  plan) exit "${FAKE_TF_PLAN_CODE:-0}" ;;
  show) if [[ "$2" == "-json" ]]; then cat "${FAKE_TF_PLAN}/plan.json"; else cat "${FAKE_TF_PLAN}/plan.txt"; fi ;;
  apply) exit "${FAKE_TF_APPLY_CODE:-0}" ;;
esac
EOF
chmod +x "${sandbox}/bin/gh" "${sandbox}/bin/terraform"
echo '{"pull_request":{"number":42}}' > "${sandbox}/event.json"
ws="${sandbox}/ws"

# new_ws: empty workspace with a stack/ directory; tests add files before run_tfpr.
new_ws() {
  rm -rf "${ws}"
  mkdir -p "${ws}/stack"
}

# run_tfpr CMD [VAR=value...]: run tfpr CMD in ${ws} as a first-attempt
# push build of refs/heads/main at sha "abc", which is also the branch head.
# Sets RC, OUT (stdout+stderr), OUTPUTS (step outputs), CALLS (gh/terraform);
# the last posted comment body is left in ${sandbox}/body.
run_tfpr() {
  local cmd="$1"
  shift
  : > "${sandbox}/log"
  : > "${sandbox}/outputs"
  rm -f "${sandbox}/body"
  RC=0
  (cd "${ws}" && env PATH="${sandbox}/bin:${PATH}" FAKE_LOG="${sandbox}/log" FAKE_BODY="${sandbox}/body" \
    GITHUB_ACTIONS=true GITHUB_OUTPUT="${sandbox}/outputs" GITHUB_REPOSITORY=o/r \
    GITHUB_RUN_ID=1 GITHUB_RUN_ATTEMPT=1 GITHUB_SERVER_URL=https://github.example \
    GITHUB_SHA=abc GITHUB_REF=refs/heads/main GITHUB_ACTOR=me RUNNER_NAME=r \
    GITHUB_EVENT_NAME=push GITHUB_EVENT_PATH="${sandbox}/event.json" \
    FAKE_BRANCH_HEAD=abc FAKE_PR=42 TFPR_STACK=stack TFPR_WORKSPACE=default \
    "$@" "${tfpr}" "${cmd}") > "${sandbox}/stdout" 2>&1 || RC=$?
  OUT="$(cat "${sandbox}/stdout")"
  OUTPUTS="$(cat "${sandbox}/outputs")"
  CALLS="$(cat "${sandbox}/log")"
}

# assert_has NAME HAYSTACK NEEDLE / assert_lacks NAME HAYSTACK NEEDLE
assert_has() {
  if [[ "$2" == *"$3"* ]]; then pass; else fail "$1: expected to contain '$3' in:"$'\n'"$2"; fi
}
assert_lacks() {
  if [[ "$2" != *"$3"* ]]; then pass; else fail "$1: expected not to contain '$3' in:"$'\n'"$2"; fi
}

# assert_golden NAME FILE [ACTUAL]: ACTUAL (default: the last posted comment
# body) equals test/fixtures/FILE.
assert_golden() {
  local golden="${root}/test/fixtures/$2" actual="${3:-${sandbox}/body}"
  if [[ "${update}" == "true" ]]; then cp "${actual}" "${golden}"; fi
  if diff -u "${golden}" "${actual}"; then pass; else fail "$1"; fi
}

# plan: runs terraform in the stack, writes the plan files and step outputs.
new_ws
run_tfpr plan FAKE_TF_PLAN="${root}/test/fixtures/render/changes" FAKE_TF_PLAN_CODE=2
assert_eq "plan: exit code" 0 "${RC}"
assert_has "plan: validates" "${CALLS}" "terraform validate -no-color"
assert_has "plan: plans" "${CALLS}" "terraform plan -input=false -no-color -detailed-exitcode -out=tfplan"
assert_golden "plan: step outputs" plan/outputs "${sandbox}/outputs"
# created_at is the only field that varies between runs.
sed 's/"created_at": "[^"]*"/"created_at": "<now>"/' "${ws}/stack/plan-metadata.json" > "${sandbox}/metadata"
assert_golden "plan: plan-metadata.json" plan/plan-metadata.json "${sandbox}/metadata"
if cmp -s "${root}/test/fixtures/render/changes/plan.txt" "${ws}/stack/plan.txt"; then pass; else fail "plan: plan.txt"; fi

new_ws
run_tfpr plan FAKE_TF_PLAN_CODE=1
assert_eq "plan error: exit code" 1 "${RC}"
assert_eq "plan error: outputs" "exit_code=1" "${OUTPUTS}"
assert_lacks "plan error: no show" "${CALLS}" "terraform show"

# Comment failures are best effort: they warn and keep the command's result.
new_ws
run_tfpr apply FAKE_GH_FAIL_WRITE=1
assert_eq "apply ok + comment fails: exit code" 0 "${RC}"
assert_has "apply ok + comment fails: warns" "${OUT}" "::warning::Could not post the apply-result comment"
assert_has "apply ok + comment fails: output" "${OUTPUTS}" "apply_exit_code=0"

new_ws
run_tfpr apply FAKE_GH_FAIL_WRITE=1 FAKE_TF_APPLY_CODE=3
assert_eq "apply fails + comment fails: exit code" 3 "${RC}"

new_ws
run_tfpr apply FAKE_TF_APPLY_CODE=3 GITHUB_RUN_ATTEMPT=2 TFPR_USE_PLANFILE=true TFPR_PLAN_RUN_ID=77
assert_eq "re-run apply fails: exit code" 3 "${RC}"
assert_golden "re-run apply fails: apply comment" comments/apply-rerun-failed.md

new_ws
cp "${root}/test/fixtures/render/no-changes/plan."* "${ws}/stack/"
run_tfpr comment FAKE_GH_FAIL_WRITE=1 GITHUB_EVENT_NAME=pull_request TFPR_EXIT_CODE=0
assert_eq "plan comment fails: exit code" 0 "${RC}"
assert_has "plan comment fails: warns" "${OUT}" "::warning::Could not post the plan comment on PR #42"

new_ws
cp "${root}/test/fixtures/render/no-changes/plan."* "${ws}/stack/"
run_tfpr comment GITHUB_EVENT_NAME=pull_request TFPR_EXIT_CODE=0
assert_eq "plan comment posts: exit code" 0 "${RC}"
assert_has "plan comment posts" "${CALLS}" "gh api -X POST repos/o/r/issues/42/comments"
assert_golden "plan comment posts: body" comments/plan.md

# A missing artifact fails closed and points at retention and the opt-in fallback.
new_ws
run_tfpr apply TFPR_USE_PLANFILE=true
assert_eq "missing artifact: exit code" 1 "${RC}"
assert_has "missing artifact: expiry hint" "${OUT}" "may have expired"
assert_has "missing artifact: names retention input" "${OUT}" "retention_days"
assert_has "missing artifact: names fallback input" "${OUT}" "allow_fresh_plan_fallback=true"
assert_lacks "missing artifact: no apply" "${CALLS}" "terraform apply"

# Planfile provenance: only a downloaded planfile for this stack/workspace counts.
# plan_artifact STACK WORKSPACE: fake a downloaded artifact in ${ws}/artifact.
plan_artifact() {
  mkdir -p "${ws}/artifact"
  echo "reviewed plan" > "${ws}/artifact/tfplan"
  jq -n --arg stack "$1" --arg workspace "$2" '{$stack, $workspace}' > "${ws}/artifact/plan-metadata.json"
}

new_ws
echo "local plan" > "${ws}/stack/tfplan"
run_tfpr apply TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact
assert_eq "stack tfplan without artifact: fails closed" 1 "${RC}"
assert_lacks "stack tfplan without artifact: no apply" "${CALLS}" "terraform apply"

new_ws
echo "local plan" > "${ws}/stack/tfplan"
plan_artifact stack default
run_tfpr apply TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact
assert_eq "matching artifact: exit code" 0 "${RC}"
assert_has "matching artifact: applies planfile" "${CALLS}" "terraform apply -input=false -no-color -lock-timeout=5m tfplan"
assert_lacks "matching artifact: no -auto-approve" "${CALLS}" "-auto-approve"
assert_eq "matching artifact: reviewed plan copied over local one" "reviewed plan" "$(cat "${ws}/stack/tfplan")"
assert_has "matching artifact: output" "${OUTPUTS}" "used_plan_artifact=true"
assert_golden "matching artifact: apply comment" comments/apply-planfile.md

new_ws
plan_artifact other-stack default
run_tfpr apply TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact
assert_eq "artifact for another stack: fails closed" 1 "${RC}"
assert_has "artifact for another stack: warns" "${OUT}" "Ignoring the planfile artifact"
assert_lacks "artifact for another stack: no apply" "${CALLS}" "terraform apply"

new_ws
plan_artifact stack prod
run_tfpr apply TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact
assert_eq "artifact for another workspace: fails closed" 1 "${RC}"

new_ws
plan_artifact stack default
rm "${ws}/artifact/plan-metadata.json"
run_tfpr apply TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact
assert_eq "artifact without metadata: fails closed" 1 "${RC}"

new_ws
echo "local plan" > "${ws}/stack/tfplan"
run_tfpr apply
assert_has "use_planfile=false ignores stack tfplan" "${CALLS}" "-lock-timeout=5m -auto-approve"

new_ws
plan_artifact stack default
run_tfpr decide TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact
assert_eq "decide empties the artifact dir" "absent" "$([[ -e "${ws}/artifact" ]] && echo present || echo absent)"
assert_has "decide requests download" "${OUTPUTS}" "download=true"

# Superseded-tree guard: every fresh apply must be the branch head.
new_ws
run_tfpr apply
assert_eq "first attempt at branch head: exit code" 0 "${RC}"
assert_has "first attempt at branch head: checks head" "${CALLS}" "gh api repos/o/r/git/ref/heads/main"
assert_has "first attempt at branch head: applies" "${CALLS}" "-auto-approve"

new_ws
run_tfpr apply FAKE_BRANCH_HEAD=def
assert_eq "first attempt, branch moved: refused" 1 "${RC}"
assert_has "first attempt, branch moved: error" "${OUT}" "::error::Fresh apply refused (attempt=1): commit abc is no longer the head of 'main' (now def)"
assert_lacks "first attempt, branch moved: no apply" "${CALLS}" "terraform apply"
assert_has "first attempt, branch moved: reason" "${OUTPUTS}" "fallback_reason=superseded"
assert_golden "first attempt, branch moved: apply comment" comments/apply-superseded.md

new_ws
run_tfpr apply GITHUB_RUN_ATTEMPT=2 TFPR_USE_PLANFILE=true
assert_eq "re-run at branch head: exit code" 0 "${RC}"
assert_has "re-run at branch head: fresh apply" "${CALLS}" "-auto-approve"
assert_has "re-run at branch head: reason" "${OUTPUTS}" "fallback_reason=rerun"

new_ws
run_tfpr apply GITHUB_RUN_ATTEMPT=2 FAKE_BRANCH_HEAD=def
assert_eq "re-run, branch moved: refused" 1 "${RC}"
assert_lacks "re-run, branch moved: no apply" "${CALLS}" "terraform apply"

new_ws
run_tfpr apply GITHUB_REF=refs/tags/v1
assert_eq "non-branch first attempt: allowed" 0 "${RC}"
assert_has "non-branch first attempt: warns" "${OUT}" "::warning::Fresh apply of non-branch ref 'refs/tags/v1'"
assert_has "non-branch first attempt: applies" "${CALLS}" "-auto-approve"
assert_lacks "non-branch first attempt: no head lookup" "${CALLS}" "git/ref/heads"

new_ws
run_tfpr apply GITHUB_REF=refs/tags/v1 GITHUB_RUN_ATTEMPT=2
assert_eq "non-branch re-run: refused" 1 "${RC}"
assert_has "non-branch re-run: reason" "${OUTPUTS}" "fallback_reason=unverified-ref"
assert_lacks "non-branch re-run: no apply" "${CALLS}" "terraform apply"

new_ws
plan_artifact stack default
run_tfpr apply TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact FAKE_BRANCH_HEAD=def
assert_eq "planfile apply skips the guard: exit code" 0 "${RC}"
assert_lacks "planfile apply skips the guard: no head lookup" "${CALLS}" "git/ref/heads"

# resolve-plan-run: merge commit -> its PR -> that PR's plan run.
new_ws
run_tfpr resolve-plan-run TFPR_PLAN_WORKFLOW=pr.yaml FAKE_PLAN_RUN=99
assert_eq "resolve: exit code" 0 "${RC}"
assert_has "resolve: queries the head sha" "${CALLS}" "repos/o/r/actions/workflows/pr.yaml/runs?head_sha=headsha&status=success&per_page=1"
assert_eq "resolve: outputs" $'run_id=99\npr_number=42' "${OUTPUTS}"

new_ws
run_tfpr resolve-plan-run TFPR_PLAN_WORKFLOW=pr.yaml
assert_has "resolve, no run: notice" "${OUT}" "::notice::No successful 'pr.yaml' run found for head_sha=headsha (PR #42)"
assert_eq "resolve, no run: outputs" $'run_id=\npr_number=42' "${OUTPUTS}"

new_ws
run_tfpr resolve-plan-run TFPR_PLAN_WORKFLOW=pr.yaml FAKE_PR=
assert_has "resolve, no PR: notice" "${OUT}" "::notice::No PR associated with abc"
assert_eq "resolve, no PR: outputs" $'run_id=\npr_number=' "${OUTPUTS}"

new_ws
run_tfpr resolve-plan-run TFPR_PLAN_WORKFLOW=pr.yaml GITHUB_RUN_ATTEMPT=2
assert_has "resolve, re-run: notice" "${OUT}" "::notice::Re-run detected (attempt=2)"
assert_lacks "resolve, re-run: no lookups" "${CALLS}" "gh api"

# --- action wrappers (static checks) ----------------------------------------
# assert_yaml NAME FILE LINE: FILE contains LINE (whitespace-trimmed) verbatim.
# LINE is literal GitHub expression text, so callers single-quote it.
assert_yaml() {
  if sed 's/^[[:space:]]*//' "${root}/$2" | grep -qxF "$3"; then pass; else fail "$1: '$3' not in $2"; fi
}
# A blank plan_artifact_run_id must mean the current run, not an empty run-id.
# shellcheck disable=SC2016
assert_yaml "apply run-id falls back to current run" apply/action.yml \
  'run-id: ${{ inputs.plan_artifact_run_id || github.run_id }}'

# shellcheck disable=SC2016
assert_yaml "plan artifact retention is an input" plan/action.yml \
  'retention-days: ${{ inputs.retention_days }}'
assert_eq "plan retention_days default" 'default: "7"' \
  "$(grep -A6 '^  retention_days:' "${root}/plan/action.yml" | grep -m1 'default:' | sed 's/^ *//')"

# shellcheck disable=SC2016
assert_yaml "apply downloads outside the stack" apply/action.yml \
  'path: ${{ runner.temp }}/tfpr-plan-artifact'

echo "${passes} passed, ${failures} failed"
[[ "${failures}" -eq 0 ]]
