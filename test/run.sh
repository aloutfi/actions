#!/usr/bin/env bash
# Plain-bash tests: golden files for `tfpr render`, plus unit tests for the
# pure helpers. Usage: test/run.sh [--update]   (--update rewrites goldens)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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
    "${root}/bin/tfpr" render > "${actual}"
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
# shellcheck source=/dev/null
source "${root}/bin/tfpr"

# decide_plan_source USE_PLANFILE ATTEMPT HAS_PLANFILE ALLOW_FALLBACK
assert_eq "use_planfile=false" "fresh no-artifact-requested" "$(decide_plan_source false 1 true false)"
assert_eq "first attempt with artifact" "planfile" "$(decide_plan_source true 1 true false)"
assert_eq "first attempt, missing artifact, fails closed" "none missing-artifact" "$(decide_plan_source true 1 false false)"
assert_eq "first attempt, missing artifact, fallback allowed" "fresh missing-artifact" "$(decide_plan_source true 1 false true)"
assert_eq "re-run ignores stored planfile" "fresh rerun" "$(decide_plan_source true 2 true false)"
assert_eq "re-run with fallback allowed" "fresh rerun" "$(decide_plan_source true 3 false true)"

# plan_stats PLAN_JSON -> TSV in PLAN_STATS order (tabs shown as |)
stats() { plan_stats "${root}/test/fixtures/render/$1/plan.json" | tr '\t' '|'; }
assert_eq "stats no-changes" "0|0|0|0|0|0|0|0|false|false|false|0 to add, 0 to change, 0 to destroy, 0 to replace, 0 to import" "$(stats no-changes)"
assert_eq "stats changes" "1|1|1|1|0|0|0|0|true|false|false|1 to add, 1 to change, 1 to destroy, 1 to replace, 0 to import" "$(stats changes)"
assert_eq "stats output-only" "0|0|0|0|0|0|0|1|false|false|true|0 to add, 0 to change, 0 to destroy, 0 to replace, 0 to import" "$(stats output-only)"
# Forget/move-only plans exit 2 and must not render as "No changes".
assert_eq "stats moved-removed" "0|0|0|0|0|2|1|0|true|false|false|0 to add, 0 to change, 0 to destroy, 0 to replace, 0 to import, 2 to forget, 1 to move" "$(stats moved-removed)"

assert_eq "artifact name default stack" "tfplan-terraform-default" "$(artifact_name terraform/ default)"
assert_eq "artifact name nested stack" "tfplan-stacks-net-core-prod" "$(artifact_name ./stacks/net.core/ prod)"
assert_eq "artifact name empty workspace" "tfplan-infra-default" "$(artifact_name infra '')"

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
    if [[ -n "${FAKE_GH_FAIL_WRITE:-}" ]]; then echo "HTTP 403: forbidden" >&2; exit 1; fi ;;
  *"/actions/runs/"*"/jobs"*) echo '{"jobs":[]}' ;;
  *"/issues/"*"/comments"*) echo '[]' ;;
  *"/commits/"*"/pulls"*) [[ -z "${FAKE_PR:-}" ]] || echo "${FAKE_PR} headsha" ;;
  *"/git/ref/heads/"*) echo "${FAKE_BRANCH_HEAD}" ;;
  *) echo "fake gh: unexpected call: $*" >&2; exit 1 ;;
esac
EOF
cat > "${sandbox}/bin/terraform" <<'EOF'
#!/usr/bin/env bash
# Stub terraform: logs each call; apply exits with FAKE_TF_APPLY_CODE.
echo "terraform $*" >> "${FAKE_LOG}"
case "$1" in
  version) echo '{"terraform_version":"0.0.0"}' ;;
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

# run_tfpr CMD [VAR=value...]: run bin/tfpr CMD in ${ws} as a first-attempt
# push build of refs/heads/main at sha "abc", which is also the branch head.
# Sets RC, OUT (stdout+stderr), OUTPUTS (step outputs), CALLS (gh/terraform).
run_tfpr() {
  local cmd="$1"
  shift
  : > "${sandbox}/log"
  : > "${sandbox}/outputs"
  RC=0
  (cd "${ws}" && env PATH="${sandbox}/bin:${PATH}" FAKE_LOG="${sandbox}/log" \
    GITHUB_ACTIONS=true GITHUB_OUTPUT="${sandbox}/outputs" GITHUB_REPOSITORY=o/r \
    GITHUB_RUN_ID=1 GITHUB_RUN_ATTEMPT=1 GITHUB_SERVER_URL=https://github.example \
    GITHUB_SHA=abc GITHUB_REF=refs/heads/main GITHUB_ACTOR=me RUNNER_NAME=r \
    GITHUB_EVENT_NAME=push GITHUB_EVENT_PATH="${sandbox}/event.json" \
    FAKE_BRANCH_HEAD=abc FAKE_PR=42 TFPR_STACK=stack TFPR_WORKSPACE=default \
    "$@" "${root}/bin/tfpr" "${cmd}") > "${sandbox}/stdout" 2>&1 || RC=$?
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
cp "${root}/test/fixtures/render/no-changes/plan."* "${ws}/stack/"
run_tfpr comment FAKE_GH_FAIL_WRITE=1 GITHUB_EVENT_NAME=pull_request TFPR_EXIT_CODE=0
assert_eq "plan comment fails: exit code" 0 "${RC}"
assert_has "plan comment fails: warns" "${OUT}" "::warning::Could not post the plan comment on PR #42"

new_ws
cp "${root}/test/fixtures/render/no-changes/plan."* "${ws}/stack/"
run_tfpr comment GITHUB_EVENT_NAME=pull_request TFPR_EXIT_CODE=0
assert_eq "plan comment posts: exit code" 0 "${RC}"
assert_has "plan comment posts" "${CALLS}" "gh api -X POST repos/o/r/issues/42/comments"

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

echo "${passes} passed, ${failures} failed"
[[ "${failures}" -eq 0 ]]
