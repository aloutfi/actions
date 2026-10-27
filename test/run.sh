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
assert_eq "stats no-changes" "0|0|0|0|0|0|false|false|false|0 to add, 0 to change, 0 to destroy, 0 to replace, 0 to import" "$(stats no-changes)"
assert_eq "stats changes" "1|1|1|1|0|0|true|false|false|1 to add, 1 to change, 1 to destroy, 1 to replace, 0 to import" "$(stats changes)"
assert_eq "stats output-only" "0|0|0|0|0|1|false|false|true|0 to add, 0 to change, 0 to destroy, 0 to replace, 0 to import" "$(stats output-only)"

assert_eq "artifact name default stack" "tfplan-terraform-default" "$(artifact_name terraform/ default)"
assert_eq "artifact name nested stack" "tfplan-stacks-net-core-prod" "$(artifact_name ./stacks/net.core/ prod)"
assert_eq "artifact name empty workspace" "tfplan-infra-default" "$(artifact_name infra '')"

echo "${passes} passed, ${failures} failed"
[[ "${failures}" -eq 0 ]]
