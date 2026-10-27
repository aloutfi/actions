# shellcheck shell=bash
# CI adapter: GitHub Actions. Contract: lib/README.md.

ci_output() { printf '%s=%s\n' "$1" "$2" >> "${GITHUB_OUTPUT}"; }
ci_warn() { echo "::warning::$*"; }
ci_error() { echo "::error::$*"; }
ci_notice() { echo "::notice::$*"; }

ci_attempt() { echo "${GITHUB_RUN_ATTEMPT:-1}"; }
ci_run_id() { echo "${GITHUB_RUN_ID}"; }
ci_run_url() { echo "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}"; }
ci_sha() { echo "${GITHUB_SHA}"; }
ci_ref() { echo "${GITHUB_REF}"; }
ci_repo() { echo "${GITHUB_REPOSITORY}"; }
ci_actor() { echo "${GITHUB_ACTOR}"; }

# PR number for pull_request builds, empty otherwise.
ci_pr() {
  [[ "${GITHUB_EVENT_NAME:-}" == "pull_request" ]] || return 0
  jq -r '.pull_request.number // empty' "${GITHUB_EVENT_PATH}"
}

# Link to this job's logs, falling back to the run URL. The Actions context
# has no job id, but the API does: match on runner_name, which is unique while
# the job is in progress and also disambiguates matrix cells.
ci_job_url() {
  local run_url id
  run_url="$(ci_run_url)"
  id="$(gh api --paginate \
    "repos/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}/jobs?per_page=100" 2> /dev/null |
    jq -r --arg runner "${RUNNER_NAME:-}" '.jobs[] | select(.runner_name == $runner) | .id' |
    head -n 1 || true)"
  if [[ -n "${id}" ]]; then echo "${run_url}/job/${id}"; else echo "${run_url}"; fi
}
