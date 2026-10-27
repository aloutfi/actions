# actions

Terraform plan → PR comment → apply, as one static Go binary (`tfpr`,
standard library only) with thin GitHub Actions wrappers.

- `plan`: init, validate, and plan. Posts or updates one summary comment per
  stack and workspace on the PR. Can upload the binary planfile as an artifact.
- `apply`: applies the reviewed planfile from that artifact, or a fresh plan,
  then posts the result on the PR that produced the commit.
- `resolve-plan-run`: finds the PR's plan run for a merge commit, so apply can
  download that run's planfile.

Azure DevOps support is in progress.

## Usage

Pin the actions to a release tag (`@vX.Y.Z`) or to that tag's commit SHA.
A release ref downloads a prebuilt `tfpr` that is checked against the
checksums committed at that ref. Any other ref, such as `@main` or a branch,
builds `tfpr` from source on every run. That takes longer, and it installs Go
with `actions/setup-go`, which puts Go on the job's `PATH` for the steps that
follow.

```yaml
# On pull_request: plan and comment
jobs:
  plan:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write # plan comment
      actions: read        # job-log link in the comment
    env:
      EXAMPLE_PROVIDER_TOKEN: ${{ secrets.EXAMPLE_PROVIDER_TOKEN }} # provider credentials: job-level env
    steps:
      - uses: actions/checkout@v6
      - uses: aloutfi/actions/plan@vX.Y.Z # a release tag, or its commit SHA
        with:
          stack: infra/
          workspace: prod
          output_planfile: "true"
```

```yaml
# On push to main: apply the planfile reviewed on the PR
jobs:
  apply:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write # apply-result comment
      actions: read        # cross-run artifact download
    env:
      EXAMPLE_PROVIDER_TOKEN: ${{ secrets.EXAMPLE_PROVIDER_TOKEN }}
    steps:
      - uses: actions/checkout@v6
      - id: plan-run
        uses: aloutfi/actions/resolve-plan-run@vX.Y.Z # a release tag, or its commit SHA
        with:
          plan_workflow_filename: pr.yaml
      - uses: aloutfi/actions/apply@vX.Y.Z # a release tag, or its commit SHA
        with:
          stack: infra/
          workspace: prod
          use_planfile: ${{ steps.plan-run.outputs.run_id != '' }}
          plan_artifact_run_id: ${{ steps.plan-run.outputs.run_id }}
```

### Credentials

Set provider credentials as job-level `env:`. Composite actions inherit it.
Step-level `env:` on the `uses:` step does not reach the action's inner steps.

The `token` input (default `github.token`) is used only for GitHub API calls.
It is not exposed to terraform: tfpr receives it as `TFPR_GITHUB_TOKEN` and
removes every `TFPR_*` variable from terraform's environment. A `GITHUB_TOKEN`
you set in the job's `env:` for the GitHub provider still reaches terraform. If you pass a GitHub App token, set
`comment_author` to the App's bot login (for example, `my-app[bot]`) so the
comment upsert finds its own comments.

Private module sources over SSH: start an agent in an earlier step, for example
`webfactory/ssh-agent`, or `ssh-agent` plus `ssh-add` exported via `$GITHUB_ENV`.

### Redaction: `TFPR_FILTER`

Set `TFPR_FILTER` (job-level `env:` for the actions) to a command that reads
text on stdin and writes the redacted text on stdout. tfpr runs it with
`sh -c` over:

- all terraform output that tfpr echoes to the log;
- the plan text it stores in the plan dir;
- the resource addresses and output names it copies from `plan.json` into
  `plan-result.json` (for_each keys can be sensitive);
- the human-readable body of every comment it posts, and of `tfpr render`'s
  output.

The first two lines of a comment are tfpr's own and are never filtered:
the marker that finds the comment again (`<!-- tf-plan:<stack> :: <workspace> -->`)
and the record. They carry only the stack, the workspace, the commit and tree
ids, unit paths, statuses and hashes. Plan text can't forge them: lines that
look like a marker or record are removed from the body.

It fails closed. A non-zero exit, a two-minute timeout, or empty output for
non-empty input fails the command, and nothing is posted. Filters must be
idempotent, because the plan text is filtered once when it is stored and
again inside the comment body. They must also keep one output line per input
line, because names are filtered one per line. The filter does not see tfpr's
own `TFPR_*` variables. `plan-result.json` records whether a filter ran
(`redaction.mode`: `filter`, `none`, or `byo`).

```sh
# Mask anything that looks like a key or a password assignment.
export TFPR_FILTER='sed -E -e "s/(AKIA[0-9A-Z]{16})/[redacted]/g" -e "s/(password *= *).*/\1[redacted]/"'
```

`plan.json` is never filtered or published. It holds sensitive values in
clear text, so tfpr reads only its structure (addresses, actions, imports,
moves, output names) and never copies a value out of it.

CI command lines never reach a comment, with or without a filter. A CI
command line is any line that starts, after optional whitespace, with
`##vso[` (Azure Pipelines) or `::` (GitHub Actions); a scrubber's report on
stdout or a value in the plan can produce one. tfpr removes every such line
from filter output and plan text:

- `##vso[task.logissue type=warning|error]msg`, `::warning::msg` and
  `::error::msg` are re-emitted through tfpr's own log, escaped.
- Every other command line is dropped, and one warning says how many.

Terraform output that tfpr echoes is wrapped in `::stop-commands::` on
GitHub, so nothing in it runs as a workflow command. `tfpr render` writes
the body as is (redirect it to a file); command lines are already stripped
from it.

## Bring your own plan

If you run terraform yourself (your own wrapper, your own scrubber, your own
CI task with cloud credentials), give tfpr the two outputs and let it do the
rest. The plan step and the comment step can then hold different
credentials:

```sh
# Step 1: cloud credentials only. tfpr is not involved.
rm -f plan.json plan.txt plan.exitcode
code=0
terraform -chdir=infra plan -input=false -no-color -detailed-exitcode -out=tfplan || code=$?
echo "$code" > plan.exitcode
[ "$code" -ne 1 ] || exit 1
terraform -chdir=infra show -json tfplan > plan.json             # stdout only; never 2>&1
terraform -chdir=infra show -no-color tfplan | ./scrub > plan.txt

# Step 2: the repo host token only (here a GitHub token), no cloud credentials.
# Run it even when step 1 failed (always()), so a failed plan replaces the
# last good one on the PR.
export TFPR_STACK=infra TFPR_FILTER=./scrub
failed=0
TFPR_PLAN_JSON=plan.json TFPR_PLAN_TXT=plan.txt TFPR_EXIT_CODE="$(cat plan.exitcode 2>/dev/null)" \
  tfpr derive || failed=1
TFPR_GITHUB_TOKEN="$VCS_TOKEN" tfpr comment
exit "$failed"
```

- `tfpr derive` reads `TFPR_PLAN_JSON` (`terraform show -json`) and
  `TFPR_PLAN_TXT` (`terraform show -no-color`). It optionally reads
  `TFPR_EXIT_CODE` (the plan's `-detailed-exitcode`) and `TFPR_PLANFILE` (the
  binary planfile, which is hashed, not copied). It writes the plan dir and
  sets the same step outputs as `plan`.
- `plan.json` must be exactly one JSON document. Anything else, such as stderr
  merged into stdout or a line-based scrubber that rewrote it, fails with a
  clear error instead of a guess.
- A failed plan still produces a result. `TFPR_EXIT_CODE=1`, a missing input,
  an invalid `plan.json` or a filter failure makes `derive` write a
  `plan-result.json` with status `error` and the reason, then exit non-zero.
  `comment` then posts "Plan failed for `<stack>`; see <run>" over the stack's
  comment, so the last good plan never stands as if current. If there is no
  plan dir at all, `comment` records and posts the failure itself, then exits
  non-zero. `tfpr plan` does the same when terraform fails, and the plan
  action's comment step runs with `always()`.
- If the two steps run in different jobs, run `derive` in step 1 and hand over
  only the plan dir (below), never `plan.json`.
- `derive` and `render` work anywhere. `comment` needs a pull request from a
  supported CI and repo host: GitHub Actions and GitHub today, with Azure
  DevOps in progress.

### The plan dir

`tfpr plan` and `tfpr derive` write the plan dir, `$TFPR_DIR` (default
`<stack>/.tfpr`). `render` and `comment` read only this directory, so they
need no terraform and no cloud credentials:

```
plan-result.json        what was planned: units, counts, statuses, planfile hash; no values
units/<path>/plan.txt   each unit's plan text, already filtered (plain terraform: units/plan.txt)
```

`plan-result.json` follows [schema/plan-result.v1.json](schema/plan-result.v1.json)
(`tfpr/plan-result@1`). Plain terraform is one unit at path `.`. The PR
comment carries a one-line hidden record of it (`<!-- tfpr:plan@1 {...} -->`,
right after the comment's marker line): the key, the commit and tree, the
status, the units that still need an apply, and the planfile hash. A later
merge-time stage reads it back from the bot's own comment.

## Inputs

### plan

| Input | Default | |
|---|---|---|
| `stack` | `terraform/` | Terraform working directory. Cleaned before use, so `infra/`, `./infra` and `infra` are the same stack in comments, metadata and artifact names |
| `workspace` | `default` | Selected with `workspace select -or-create` |
| `terraform_version` | `1.15.3` | |
| `output_planfile` | `false` | Upload `tfplan` + `plan-metadata.json` as `tfplan-<stack-slug>-<workspace>` |
| `retention_days` | `7` | How long the planfile artifact is kept. Apply fails if it has expired by merge time. |
| `comment_author` | `github-actions[bot]` | Only comments by this login are updated |
| `token` | `github.token` | GitHub API token |

Outputs: `exit_code`, `has_changes`, `has_imports`, `has_output_changes`,
`summary`, `plan_artifact_name`.

### apply

| Input | Default | |
|---|---|---|
| `stack`, `workspace`, `terraform_version` | | Same as plan |
| `use_planfile` | `false` | Download and apply the plan action's planfile |
| `allow_fresh_plan_fallback` | `false` | If the planfile is missing on the first attempt, apply a fresh plan instead of failing |
| `plan_artifact_run_id` | current run | Run that uploaded the planfile |
| `lock_timeout` | `5m` | `terraform apply -lock-timeout` |
| `comment_author`, `token` | | Same as plan |

Outputs: `apply_exit_code`, `used_plan_artifact`, `fallback_reason`.

How apply chooses what to run:

| Case | Result |
|---|---|
| `use_planfile: false` | Fresh plan, `-auto-approve` |
| First attempt, planfile downloaded for this stack and workspace | Apply the planfile |
| First attempt, planfile missing or for another stack/workspace | **Fail**, or a fresh plan with `allow_fresh_plan_fallback: true` |
| Re-run (attempt > 1) | Fresh plan |

The planfile is downloaded outside the checkout, and its `plan-metadata.json`
must name the same `stack` and `workspace` as the apply. A `tfplan` already
in the stack directory is never treated as the reviewed plan.

Every fresh apply (any row above that says "fresh plan") first checks that the
run's commit is still the head of its branch. Applying an older commit's tree
with `-auto-approve` would roll back everything merged since, so:

- On a branch ref, a fresh apply of a commit that is no longer the branch head
  is refused, on any attempt. The newer run applies the newer tree.
- On a non-branch ref (tag, PR merge ref), there is no head to compare against.
  The first attempt applies with a warning; re-runs are refused.
- Planfile applies skip this check. Terraform itself rejects a planfile whose
  state has changed since it was made.

### resolve-plan-run

| Input | Default | |
|---|---|---|
| `plan_workflow_filename` | `pr.yaml` | Workflow file that ran the plan |
| `token` | `github.token` | Needs `actions: read` |

Outputs: `run_id` and `pr_number`. Both are empty on a re-run, on a direct push,
when several PRs match, or when no successful plan run exists.

### How the actions get `tfpr`

Each action's first step runs `scripts/install-tfpr.sh`, which takes the first
of these that applies:

1. `TFPR_BIN` in the job's `env:`: a path to a prebuilt `tfpr` binary.
2. A release. Release tags carry a `checksums.txt` next to the actions. The
   script downloads `tfpr_<version>_<os>_<arch>.tar.gz` from this repo's
   GitHub release and checks it against that file's sha256. Pinning
   `aloutfi/actions/plan@<tag or sha>` therefore pins the binary's bytes.
3. Otherwise (`@main`, a branch, or any unreleased commit) it builds `tfpr`
   from the action's own source with `actions/setup-go`. This changes the
   job: Go is on `PATH` for every step after the action, and the build adds
   time to each run. Pin a release to avoid both.

## Development

```sh
go test ./...                # unit tests, render goldens, command scenarios
test/run.sh                  # black-box CLI suite (builds tfpr; or set TFPR_BIN)
                             # render fixtures: test/fixtures/render/*/{plan.json,plan.txt}
                             # -> plan-result.json and expected.md goldens
test/run.sh --update         # rewrite golden files
test/fixtures/generate.sh    # regenerate plan fixtures with real terraform
gofmt -l . && go vet ./...
shellcheck scripts/install-tfpr.sh test/run.sh test/fixtures/generate.sh
actionlint
```

Layout: `core/` is pure (the PlanResult and its derivation, naming, apply
policies, comment templates), `ci/` holds the CI platform adapters, `vcs/`
holds the repo-host adapters, `redact/` runs `TFPR_FILTER`, and `cmd/tfpr`
wires them together. A comment body is a `redact.Text`, which only the
configured filter can produce; `vcs.UpsertComment` accepts nothing else. The
ports are the `CI` interface in [ci/ci.go](ci/ci.go) and the `VCS` interface
in [vcs/vcs.go](vcs/vcs.go). `test/cases/*.tsv` and the golden files under
`test/fixtures/` are the shared behavior spec.

The terraform executable is `TFPR_TF_BIN` (default `terraform`).
