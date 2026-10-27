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
      - uses: aloutfi/actions/plan@main
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
        uses: aloutfi/actions/resolve-plan-run@main
        with:
          plan_workflow_filename: pr.yaml
      - uses: aloutfi/actions/apply@main
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
It is not exposed to terraform. If you pass a GitHub App token, set
`comment_author` to the App's bot login (for example, `my-app[bot]`) so the
comment upsert finds its own comments.

Private module sources over SSH: start an agent in an earlier step, for example
`webfactory/ssh-agent`, or `ssh-agent` plus `ssh-add` exported via `$GITHUB_ENV`.

## Inputs

### plan

| Input | Default | |
|---|---|---|
| `stack` | `terraform/` | Terraform working directory |
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
must name the same `stack` and `workspace` as the apply. Stacks are compared
as cleaned paths, so `infra/`, `infra` and `./infra` match. A `tfplan`
already in the stack directory is never treated as the reviewed plan.

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
3. Otherwise (a branch, or any unreleased commit) it builds `tfpr` from the
   action's own source with `actions/setup-go`. That puts Go on the job's
   `PATH`.

## Development

```sh
go test ./...                # unit tests, render goldens, command scenarios
test/run.sh                  # black-box CLI suite (builds tfpr; or set TFPR_BIN)
test/run.sh --update         # rewrite golden files
test/fixtures/generate.sh    # regenerate plan fixtures with real terraform
gofmt -l . && go vet ./...
shellcheck scripts/install-tfpr.sh test/run.sh test/fixtures/generate.sh
actionlint
```

Layout: `core/` is pure (plan stats, naming, apply policies, comment
templates), `ci/` holds the CI platform adapters and their detection table,
`vcs/` holds the repo-host adapters, and `cmd/tfpr` wires them together. The
ports are the `CI` interface in [ci/ci.go](ci/ci.go) and the `VCS` interface
in [vcs/vcs.go](vcs/vcs.go). `test/cases/*.tsv` and the golden files under
`test/fixtures/` are the shared behavior spec.

The terraform executable is `TFPR_TF_BIN` (default `terraform`).
