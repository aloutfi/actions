# Adapter contract

`bin/tfpr` reaches the CI platform and the repo host only through the shell
functions below. An adapter is one file that defines all of them; `tfpr`
sources it at runtime.

- CI adapter `lib/ci/<name>.sh`. Selected by environment: `GITHUB_ACTIONS=true`
  selects `github`.
- VCS adapter `lib/vcs/<name>.sh`. Defaults to `github`; override with
  `TFPR_VCS=<name>`. Chosen by where the repo lives, not by which CI runs it.

Functions print their result on stdout. Values are single-line. A non-zero
exit status means the call failed.

## CI adapter (`lib/ci/*.sh`)

| Function | Args | Behavior |
|---|---|---|
| `ci_output` | `NAME VALUE` | Set a step output that later steps can read. |
| `ci_warn` | `MESSAGE` | Emit a warning annotation. |
| `ci_error` | `MESSAGE` | Emit an error annotation. Does not exit. |
| `ci_notice` | `MESSAGE` | Emit a notice annotation. |
| `ci_attempt` | | Run attempt number, `1` on the first attempt. |
| `ci_run_id` | | Run/build id. |
| `ci_run_url` | | URL of the run/build. |
| `ci_job_url` | | URL of the current job's logs. Best effort; falls back to `ci_run_url`. |
| `ci_pr` | | PR number when the build was triggered by a pull request, else empty. |
| `ci_sha` | | Commit SHA being built. |
| `ci_ref` | | Full git ref being built, e.g. `refs/heads/main`. |
| `ci_repo` | | Repository identifier the VCS adapter understands (`owner/repo` on GitHub). |
| `ci_actor` | | User who triggered the run. |

## VCS adapter (`lib/vcs/*.sh`)

`tfpr` sets `TFPR_REPO` (default: `ci_repo`) before calling these.

| Function | Args | Behavior |
|---|---|---|
| `vcs_upsert_comment` | `PR MARKER BODY_FILE [AUTHOR]` | Update the PR comment written by `AUTHOR` whose body starts with `MARKER`, or post a new one with the contents of `BODY_FILE`. Comments by anyone else are never edited. Empty `AUTHOR` means the adapter's default bot identity. |
| `vcs_pr_for_commit` | `SHA` | One line per PR associated with `SHA` (for example, the PR that merged it): `<number> <head_sha>`. Empty output if there is none. |
| `vcs_find_plan_run` | `WORKFLOW HEAD_SHA` | Id of the newest successful run of the plan pipeline `WORKFLOW` for `HEAD_SHA`, or empty. |
| `vcs_branch_head` | `BRANCH` | Current head commit SHA of `BRANCH` (short name, no `refs/heads/`). |
