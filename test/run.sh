#!/usr/bin/env bash
# Black-box tests for the tfpr CLI: golden files for `tfpr render`, the case
# tables in test/cases/ driven through the CLI, the commands that need no
# repo host, and static checks on the action wrappers. Commands that call the
# GitHub API are tested in cmd/tfpr against a fake API server.
#
# Usage: [TFPR_BIN=path/to/tfpr] test/run.sh [--update | --compat]
#   --update rewrites goldens.
#   --compat compares goldens with resource ids, hashes and the terraform
#     version masked, for fixtures re-planned with another terraform
#     (test/fixtures/generate.sh): every other byte must match.
# Without TFPR_BIN, tfpr is built from this checkout with `go build`.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
update=false compat=false
[[ "${1:-}" == "--update" ]] && update=true
[[ "${1:-}" == "--compat" ]] && compat=true
failures=0
passes=0

sandbox="$(mktemp -d)"
trap 'rm -rf "${sandbox}"' EXIT

tfpr="${TFPR_BIN:-}"
if [[ -z "${tfpr}" ]]; then
  tfpr="${sandbox}/tfpr"
  (cd "${root}" && go build -o "${tfpr}" ./cmd/tfpr)
fi
[[ "${tfpr}" == /* ]] || tfpr="${PWD}/${tfpr}"

pass() { passes=$((passes + 1)); }
fail() {
  failures=$((failures + 1))
  echo "FAIL: $*"
}

# assert_eq NAME EXPECTED ACTUAL
assert_eq() {
  if [[ "$2" == "$3" ]]; then pass; else fail "$1: expected '$2', got '$3'"; fi
}

# assert_has NAME HAYSTACK NEEDLE / assert_lacks NAME HAYSTACK NEEDLE
assert_has() {
  if [[ "$2" == *"$3"* ]]; then pass; else fail "$1: expected to contain '$3' in:"$'\n'"$2"; fi
}
assert_lacks() {
  if [[ "$2" != *"$3"* ]]; then pass; else fail "$1: expected not to contain '$3' in:"$'\n'"$2"; fi
}

# assert_golden NAME FILE ACTUAL: ACTUAL equals test/fixtures/FILE. A FILE
# ending in .gz is compared unzipped (large fixtures). With --compat, ids,
# hashes and the terraform version are masked on both sides.
assert_golden() {
  local golden="${root}/test/fixtures/$2"
  if [[ "${update}" == "true" ]]; then
    if [[ "${golden}" == *.gz ]]; then gzip -9n < "$3" > "${golden}"; else cp "$3" "${golden}"; fi
  fi
  if diff -u <(golden_text "${golden}") <(mask < "$3"); then pass; else fail "$1"; fi
}
golden_text() {
  if [[ "$1" == *.gz ]]; then gunzip -c "$1" | mask; else mask < "$1"; fi
}
mask() {
  if [[ "${compat}" == "true" ]]; then
    sed -E 's/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/<id>/g; s/sha256:[0-9a-f]{64}/sha256:<hash>/g
      s/"binary_version": "[^"]*"/"binary_version": "<version>"/g
      s#\[terraform [^]]*\]\(https://github\.com/hashicorp/terraform/releases/tag/v[^)]*\)#[terraform <version>](<release>)#g'
  else
    cat
  fi
}

# cases FILE: the rows of a case table in test/cases/ (tab-separated, '#'
# lines are headers), with tabs turned into \x1f so `read` keeps empty fields.
cases() { grep -v '^#' "${root}/test/cases/$1" | tr '\t' '\037'; }

# --- derive + render golden files --------------------------------------------
# Each fixture is a plan the caller made itself: derive writes the plan dir
# (golden plan-result.json), render prints the comment (golden expected.md),
# and comment, outside a PR, writes the job summary (golden summary.md).
# Optional files: stack (the stack name, when it is not the directory name)
# and budget (TFPR_COMMENT_BUDGET). Large fixtures are gzipped, goldens too.
links=(TFPR_JOB_URL="https://ci.example/run/1234/job/5678" TFPR_SUMMARY_URL="https://ci.example/run/1234#summary"
  TFPR_PLAN_TXT_URL="https://ci.example/run/1234/artifacts/plan.txt")
cd "${root}/test/fixtures/render"
for dir in */; do
  name="${dir%/}"
  stack="${name}" budget="" gz=""
  [[ -f "${name}/stack" ]] && stack="$(cat "${name}/stack")"
  [[ -f "${name}/budget" ]] && budget="$(cat "${name}/budget")"
  plan_json="${name}/plan.json" plan_txt="${name}/plan.txt"
  if [[ -f "${name}/plan.json.gz" ]]; then
    gz=.gz plan_json="${sandbox}/plan.json" plan_txt="${sandbox}/plan.txt"
    gunzip -c "${name}/plan.json.gz" > "${plan_json}"
    gunzip -c "${name}/plan.txt.gz" > "${plan_txt}"
  fi
  rm -rf "${sandbox}/plandir"
  env -i PATH="${PATH}" TFPR_STACK="${stack}" TFPR_WORKSPACE=default TFPR_DIR="${sandbox}/plandir" \
    TFPR_PLAN_JSON="${plan_json}" TFPR_PLAN_TXT="${plan_txt}" TFPR_EXIT_CODE="$(cat "${name}/exit_code")" \
    "${tfpr}" derive > /dev/null 2>&1 || fail "derive ${name}"
  # The tfpr version is the only field that depends on the build.
  sed 's/"version": "[^"]*"/"version": "<version>"/' "${sandbox}/plandir/plan-result.json" > "${sandbox}/plan-result.json"
  assert_golden "derive ${name}" "render/${name}/plan-result.json${gz}" "${sandbox}/plan-result.json"
  env -i PATH="${PATH}" TFPR_DIR="${sandbox}/plandir" "${links[@]}" TFPR_COMMENT_BUDGET="${budget}" \
    "${tfpr}" render > "${sandbox}/render.md"
  assert_golden "render ${name}" "render/${name}/expected.md" "${sandbox}/render.md"
  env -i PATH="${PATH}" TFPR_DIR="${sandbox}/plandir" TFPR_SUMMARY_FILE="${sandbox}/summary.md" \
    TFPR_PLAN_TXT_URL="https://ci.example/run/1234/artifacts/plan.txt" "${tfpr}" comment > /dev/null 2>&1 || fail "summary ${name}"
  assert_golden "summary ${name}" "render/${name}/summary.md${gz}" "${sandbox}/summary.md"
done

# The Azure Repos dialect (TFPR_VCS=azure) on the fixtures that stress the
# layout: headings instead of <details>, plain fences, no hidden markup (the
# marker and record are thread properties), the banner still right under the
# headline, and within Azure's limit.
for name in truncated large; do
  stack="${name}" budget="" plan_json="${name}/plan.json" plan_txt="${name}/plan.txt"
  [[ -f "${name}/budget" ]] && budget="$(cat "${name}/budget")"
  if [[ -f "${name}/plan.json.gz" ]]; then
    plan_json="${sandbox}/plan.json" plan_txt="${sandbox}/plan.txt"
    gunzip -c "${name}/plan.json.gz" > "${plan_json}"
    gunzip -c "${name}/plan.txt.gz" > "${plan_txt}"
  fi
  rm -rf "${sandbox}/plandir"
  env -i PATH="${PATH}" TFPR_STACK="${stack}" TFPR_WORKSPACE=default TFPR_DIR="${sandbox}/plandir" \
    TFPR_PLAN_JSON="${plan_json}" TFPR_PLAN_TXT="${plan_txt}" TFPR_EXIT_CODE="$(cat "${name}/exit_code")" \
    "${tfpr}" derive > /dev/null 2>&1 || fail "derive ${name} (azure)"
  env -i PATH="${PATH}" TFPR_DIR="${sandbox}/plandir" TFPR_VCS=azure "${links[@]}" TFPR_COMMENT_BUDGET="${budget}" \
    "${tfpr}" render > "${sandbox}/render.md"
  assert_golden "render ${name} (azure)" "render/${name}/expected-azure.md" "${sandbox}/render.md"
  out="$(cat "${sandbox}/render.md")"
  assert_lacks "${name} (azure): no <details>" "${out}" "<details"
  assert_lacks "${name} (azure): no diff fence" "${out}" '```diff'
  assert_lacks "${name} (azure): no hidden markup" "${out}" "<!--"
  if head -n 4 "${sandbox}/render.md" | grep -q '^> \*\*'; then pass; else
    fail "${name} (azure): the banner is not right under the headline and status line"; fi
  chars="$(LC_ALL=en_US.UTF-8 wc -m < "${sandbox}/render.md" | tr -d ' ')"
  if [[ "${chars}" -le 140000 ]]; then pass; else fail "${name} (azure): ${chars} characters, over 140,000"; fi
done

# The truncated and large fixtures must be cut, and say so up front: the
# banner is right under the headline and status line (the comment's first six
# lines), every delete is listed, and the whole comment fits GitHub's limit.
for name in truncated large; do
  if head -n 6 "${name}/expected.md" | grep -q '^> \*\*Plan text truncated\*\*'; then pass; else
    fail "${name}: no truncation banner in the first six lines"; fi
done
# Bytes bound characters from above.
size="$(wc -c < large/expected.md | tr -d ' ')"
if [[ "${size}" -le 65536 ]]; then pass; else fail "large: ${size} bytes, over GitHub's 65,536 characters"; fi
# shellcheck disable=SC2016 # literal backticks
list="$(sed -n '/^```diff$/,/^```$/p' large/expected.md)"
missing_deletes="$(gunzip -c large/plan.txt.gz | sed -n 's/^  # \(.*\) will be destroyed$/\1/p' |
  while read -r address; do grep -qF -- " ${address} " <<< "${list}" || echo "${address}"; done)"
assert_eq "large: every destroy is listed" "" "${missing_deletes}"
cd "${root}"

# --- commands with a stub terraform ------------------------------------------
cat > "${sandbox}/terraform" << 'EOF'
#!/usr/bin/env bash
# Stub terraform: logs each call; plan and apply exit with FAKE_TF_PLAN_CODE
# and FAKE_TF_APPLY_CODE; show prints the plan from FAKE_TF_PLAN (a fixture dir).
echo "terraform $*" >> "${FAKE_LOG}"
case "$1" in
  version) echo '{"terraform_version":"0.0.0"}' ;;
  plan) echo "binary plan" > tfplan; exit "${FAKE_TF_PLAN_CODE:-0}" ;;
  show) if [[ "$2" == "-json" ]]; then cat "${FAKE_TF_PLAN}/plan.json"; else cat "${FAKE_TF_PLAN}/plan.txt"; fi ;;
  apply) exit "${FAKE_TF_APPLY_CODE:-0}" ;;
esac
EOF
chmod +x "${sandbox}/terraform"
ws="${sandbox}/ws"

# new_ws: empty workspace with a stack/ directory.
new_ws() {
  rm -rf "${ws}"
  mkdir -p "${ws}/stack"
}

# run_tfpr CMD [VAR=value...]: run tfpr CMD in ${ws} as a GitHub Actions
# push build of refs/heads/main at sha "abc", with a clean environment.
# Sets RC, OUT (stdout+stderr), OUTPUTS (step outputs), CALLS (terraform).
run_tfpr() {
  local cmd="$1"
  shift
  : > "${sandbox}/log"
  : > "${sandbox}/outputs"
  RC=0
  (cd "${ws}" && env -i PATH="${PATH}" HOME="${HOME}" FAKE_LOG="${sandbox}/log" \
    TFPR_TF_BIN="${sandbox}/terraform" \
    GITHUB_ACTIONS=true GITHUB_OUTPUT="${sandbox}/outputs" GITHUB_REPOSITORY=o/r \
    GITHUB_RUN_ID=1 GITHUB_RUN_ATTEMPT=1 GITHUB_SERVER_URL=https://github.example \
    GITHUB_SHA=abc GITHUB_REF=refs/heads/main GITHUB_ACTOR=me RUNNER_NAME=r \
    GITHUB_EVENT_NAME=push TFPR_STACK=stack TFPR_WORKSPACE=default GITHUB_API_URL=http://127.0.0.1:9 \
    "$@" "${tfpr}" "${cmd}") > "${sandbox}/stdout" 2>&1 || RC=$?
  OUT="$(cat "${sandbox}/stdout")"
  OUTPUTS="$(cat "${sandbox}/outputs")"
  CALLS="$(cat "${sandbox}/log")"
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
if cmp -s "${root}/test/fixtures/render/changes/plan.txt" "${ws}/stack/.tfpr/units/plan.txt"; then pass; else
  fail "plan: plan text in the plan dir"; fi
assert_has "plan: plan-result.json" "$(cat "${ws}/stack/.tfpr/plan-result.json")" '"schema": "tfpr/plan-result@1"'
if [[ -e "${ws}/stack/.tfpr/units/plan.json" ]]; then fail "plan: plan.json leaked into the plan dir"; else pass; fi

# Bring your own plan with TFPR_FILTER: the caller's text and the names from
# plan.json are filtered before anything is stored or printed.
new_ws
run_tfpr derive TFPR_PLAN_JSON="${root}/test/fixtures/render/changes/plan.json" \
  TFPR_PLAN_TXT="${root}/test/fixtures/render/changes/plan.txt" TFPR_FILTER="sed s/beta/REDACTED/g"
assert_eq "byo + filter: exit code" 0 "${RC}"
assert_lacks "byo + filter: plan text" "$(cat "${ws}/stack/.tfpr/units/plan.txt")" "beta"
assert_lacks "byo + filter: plan-result.json" "$(cat "${ws}/stack/.tfpr/plan-result.json")" "beta"
assert_has "byo + filter: mode" "$(cat "${ws}/stack/.tfpr/plan-result.json")" '"mode": "filter"'
run_tfpr render TFPR_FILTER="sed s/beta/REDACTED/g"
assert_has "byo + filter: rendered" "${OUT}" 'terraform_data.item["REDACTED"] will be destroyed'
run_tfpr render TFPR_FILTER="exit 1"
assert_eq "failing filter: render fails" 1 "${RC}"
assert_lacks "failing filter: prints no body" "${OUT}" "tf-plan:"

# A failed plan still produces a result, and render says the plan failed.
new_ws
run_tfpr derive TFPR_EXIT_CODE=1
assert_eq "byo failed plan: exit code" 1 "${RC}"
assert_has "byo failed plan: status" "$(cat "${ws}/stack/.tfpr/plan-result.json")" '"status": "error"'
run_tfpr render
# shellcheck disable=SC2016 # literal backticks
assert_has "byo failed plan: rendered" "${OUT}" '**Plan failed.** Any earlier plan for `stack` is out of date'

# derive refuses plan.json with stderr mixed in.
{ cat "${root}/test/fixtures/render/changes/plan.json"; echo "Warning: from stderr"; } > "${ws}/broken.json"
run_tfpr derive TFPR_PLAN_JSON=broken.json TFPR_PLAN_TXT="${root}/test/fixtures/render/changes/plan.txt"
assert_eq "derive, broken plan.json: exit code" 1 "${RC}"
assert_has "derive, broken plan.json: hint" "${OUT}" "stderr merged into stdout?"

new_ws
run_tfpr plan FAKE_TF_PLAN_CODE=1
assert_eq "plan error: exit code" 1 "${RC}"
assert_eq "plan error: outputs" "exit_code=1" "${OUTPUTS}"
assert_lacks "plan error: no show" "${CALLS}" "terraform show"

# plan_stats.tsv: plan's step outputs carry the statistics in table order.
while IFS=$'\037' read -r fixture expected; do
  new_ws
  run_tfpr plan FAKE_TF_PLAN="${root}/test/fixtures/render/${fixture}" FAKE_TF_PLAN_CODE=2
  assert_eq "plan_stats: ${fixture}" "${expected//$'\037'/|}" \
    "$(sed -n '2,13s/^[a-z_]*=//p' "${sandbox}/outputs" | paste -sd '|' -)"
done < <(cases plan_stats.tsv)

# artifact_name.tsv: gate names the artifact that apply downloads. A first
# attempt on a tag passes the gate without a branch-head lookup.
while IFS=$'\037' read -r name stack workspace expected; do
  new_ws
  mkdir -p "${ws}/${stack}"
  run_tfpr gate TFPR_STACK="${stack}" TFPR_WORKSPACE="${workspace}" GITHUB_REF=refs/tags/v1
  assert_has "artifact_name: ${name}" "${OUTPUTS}" "artifact_name=${expected}"
done < <(cases artifact_name.tsv)

# same_stack.tsv is checked by the Go scenario tests: a planfile apply needs
# the merged PR's trusted plan record, which only a fake API can provide.

new_ws
mkdir -p "${ws}/artifact"
echo "stale" > "${ws}/artifact/tfplan"
run_tfpr gate TFPR_USE_PLANFILE=true TFPR_PLAN_DIR=artifact GITHUB_REF=refs/tags/v1
assert_eq "gate empties the artifact dir" "absent" "$([[ -e "${ws}/artifact" ]] && echo present || echo absent)"
# No reachable API, so no trusted plan record: nothing to download, and an
# apply that wants the planfile fails closed (missing artifact).
assert_has "gate without a trusted record: no download" "${OUTPUTS}" "download=false"
assert_has "gate writes decision.json" "$(cat "${ws}/stack/.tfpr/decision.json")" '"schema": "tfpr/decision@1"'

run_tfpr gate TFPR_USE_PLANFILE=true GITHUB_RUN_ATTEMPT=2 GITHUB_REF=refs/tags/v1
assert_eq "gate on a re-run of a tag: refused" 1 "${RC}"
assert_has "gate on a re-run: no download" "${OUTPUTS}" "download=false"

run_tfpr --version
assert_eq "--version" "tfpr dev" "${OUT/tfpr v*/tfpr dev}"

run_tfpr nonsense
assert_eq "unknown command: exit code" 1 "${RC}"
assert_has "unknown command: usage" "${OUT}" "usage: tfpr plan|derive|render|comment|gate|apply|report|--version"

# --- action wrappers (static checks) ----------------------------------------
# assert_yaml NAME FILE LINE: FILE contains LINE (whitespace-trimmed) verbatim.
# LINE is literal GitHub expression text, so callers single-quote it.
assert_yaml() {
  if sed 's/^[[:space:]]*//' "${root}/$2" | grep -qxF "$3"; then pass; else fail "$1: '$3' not in $2"; fi
}
# The planfile comes only from the run the gate trusts.
# shellcheck disable=SC2016
assert_yaml "apply downloads from the gate's trusted run" apply/action.yml \
  'run-id: ${{ steps.gate.outputs.plan_run_id }}'

# shellcheck disable=SC2016
assert_yaml "plan artifact retention is an input" plan/action.yml \
  'retention-days: ${{ inputs.retention_days }}'
assert_eq "plan retention_days default" 'default: "7"' \
  "$(grep -A6 '^  retention_days:' "${root}/plan/action.yml" | grep -m1 'default:' | sed 's/^ *//')"

# shellcheck disable=SC2016
assert_yaml "the plan comment runs when the plan failed, not when cancelled" plan/action.yml \
  'if: ${{ !cancelled() }}'
# shellcheck disable=SC2016
assert_yaml "the comment links the plan.txt artifact" plan/action.yml \
  'TFPR_PLAN_TXT_URL: ${{ steps.plan-text-upload.outputs.artifact-url }}'
assert_yaml "plan.txt is uploaded unzipped" plan/action.yml 'archive: false'


# shellcheck disable=SC2016
assert_yaml "apply downloads outside the stack" apply/action.yml \
  'path: ${{ runner.temp }}/tfpr-plan-artifact'

# Every action installs tfpr first and runs only that binary.
for action in plan apply gate report; do
  # shellcheck disable=SC2016
  assert_yaml "${action} locates tfpr" "${action}/action.yml" \
    'run: '\''"${GITHUB_ACTION_PATH}/../scripts/install-tfpr.sh"'\'
  # shellcheck disable=SC2016
  assert_yaml "${action} can build tfpr" "${action}/action.yml" \
    'go-version-file: ${{ github.action_path }}/../go.mod'
  assert_lacks "${action} runs no bash core" "$(cat "${root}/${action}/action.yml")" "bin/tfpr"
done

# install-tfpr.sh: a supplied binary is used as-is, on any runner.
: > "${sandbox}/install"
GITHUB_OUTPUT="${sandbox}/install" RUNNER_OS=Plan9 RUNNER_ARCH=mips TFPR_BIN="${tfpr}" \
  "${root}/scripts/install-tfpr.sh" > /dev/null
assert_eq "install-tfpr.sh honours TFPR_BIN on any runner" "path=${tfpr}" "$(cat "${sandbox}/install")"

# Third-party actions are pinned to a full commit SHA.
unpinned="$(grep -hE '^[[:space:]-]*uses: ' "${root}"/*/action.yml "${root}"/.github/workflows/*.yml |
  grep -vE 'uses: \./|@[0-9a-f]{40}( |$)' || true)"
assert_eq "actions are pinned to SHAs" "" "${unpinned}"

echo "${passes} passed, ${failures} failed"
[[ "${failures}" -eq 0 ]]
