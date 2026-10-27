# shellcheck shell=bash
# Repo-host adapter: GitHub, via the gh CLI (auth from GH_TOKEN).
# Contract: lib/README.md. TFPR_REPO is owner/repo.

# Create or update the PR comment that starts with MARKER. Only comments by
# AUTHOR are candidates, so a human who pastes the marker into their own
# comment never has it overwritten.
vcs_upsert_comment() {
  local pr="$1" marker="$2" body_file="$3" author="${4:-github-actions[bot]}" id
  id="$(gh api --paginate "repos/${TFPR_REPO}/issues/${pr}/comments" |
    jq -r --arg author "${author}" --arg marker "${marker}" \
      '.[] | select(.user.login == $author) | select(.body | startswith($marker)) | .id' |
    head -n 1 || true)"
  if [[ -n "${id}" ]]; then
    gh api -X PATCH "repos/${TFPR_REPO}/issues/comments/${id}" -F "body=@${body_file}" > /dev/null || return 1
    echo "Updated comment ${id} on PR #${pr}"
  else
    gh api -X POST "repos/${TFPR_REPO}/issues/${pr}/comments" -F "body=@${body_file}" > /dev/null || return 1
    echo "Posted comment on PR #${pr}"
  fi
}

# One line per PR associated with SHA: "<number> <head_sha>".
vcs_pr_for_commit() {
  gh api "repos/${TFPR_REPO}/commits/$1/pulls" --jq '.[] | "\(.number) \(.head.sha)"'
}

# Id of the newest successful run of WORKFLOW (file name) for HEAD_SHA, or empty.
vcs_find_plan_run() {
  gh api "repos/${TFPR_REPO}/actions/workflows/$1/runs?head_sha=$2&status=success&per_page=1" \
    --jq '.workflow_runs[0].id // empty'
}

# Current head commit SHA of BRANCH.
vcs_branch_head() {
  gh api "repos/${TFPR_REPO}/git/ref/heads/$1" --jq .object.sha
}
