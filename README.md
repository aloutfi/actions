# actions

Terraform plan → PR comment → apply for GitHub Actions and Azure Pipelines.
One static Go binary (`tfpr`, standard library only) does the work; the
actions are thin wrappers around it.

## Table of contents

*   [Features 🚀](#features-)
*   [Usage 💻](#usage-)
    *   [On `pull_request` 🔀](#on-pull_request-)
    *   [On `push` ⬆️](#on-push-️)
    *   [Skip applies when the PR's plan had no changes ⏭️](#skip-applies-when-the-prs-plan-had-no-changes-️)
    *   [When the reviewed plan is stale 🔁](#when-the-reviewed-plan-is-stale-)
    *   [Path filters and shared modules 🗂️](#path-filters-and-shared-modules-️)
    *   [On `schedule`: drift ⏰](#on-schedule-drift-)
    *   [Azure Pipelines and your own terraform 🔷](#azure-pipelines-and-your-own-terraform-)
*   [Inputs ⚙️](#inputs-️)
*   [Outputs 📤](#outputs-)
*   [The plan comment 💬](#the-plan-comment-)
*   [Credentials 🔑](#credentials-)
*   [Terraform versions 🧱](#terraform-versions-)
*   [Versioning 🏷️](#versioning-️)
*   [Examples 📄](#examples-)
*   [Development 🛠️](#development-️)

## Features 🚀

*   One comment per stack and workspace, updated in place on every push.
*   The headline and a status line come first: what the plan does, the commit,
    and links to the job log, the full plan and `plan.txt`.
*   A plan too large for one comment says so at the top ("showing N of M
    characters"). Every delete, replace and forget is still listed.
*   A risk-ordered resource list from `plan.json`: delete, replace, forget,
    update, import, move, create, read.
*   Apply the planfile reviewed on the PR, or a fresh plan.
*   A commit whose stack a newer commit changed is a clean skip, so an older
    run never rolls back a newer merge.
*   Opt-in: skip the apply job, and its cloud credentials, when the PR's plan
    had no changes.
*   Scheduled drift checks with a job summary, outputs and a warning.
*   Redaction through your own filter, failing closed.
*   GitHub Actions and Azure Pipelines; GitHub and Azure Repos.

## Usage 💻

> [!IMPORTANT]
>
> *   **Pin a release**: `@v1`, `@v1.0`, `@v1.0.0` or a commit SHA. A release
>     ref downloads a prebuilt `tfpr` checked against the checksums committed
>     at that ref.
>     Any other ref (`@main`, a branch) builds `tfpr` from source on every run
>     and puts Go on the job's `PATH`.
> *   **Provider credentials go in job-level `env:`.** Composite actions
>     inherit it. Step-level `env:` on the `uses:` step does not reach their
>     inner steps.
> *   **Run applies one at a time** with `concurrency:`, so the newest run
>     always comes last.
> *   **Terraform 1.15.9 or later.** See [Terraform versions](#terraform-versions-).

### On [`pull_request`](https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows#pull_request) 🔀

Plan, then post or update the comment. `output_planfile` keeps the binary
planfile so the merge can apply exactly what was reviewed.

```yaml
name: plan

on:
  pull_request:

jobs:
  plan:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write # the plan comment
      actions: read        # the job-log link in the comment
    env:
      EXAMPLE_PROVIDER_TOKEN: ${{ secrets.EXAMPLE_PROVIDER_TOKEN }}
    steps:
      - uses: actions/checkout@v6
      - uses: aloutfi/actions/plan@v1
        with:
          stack: infra/
          workspace: prod
          output_planfile: "true"
```

### On [`push`](https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows#push) ⬆️

Apply the planfile reviewed on the PR. The plan run comes from tfpr's own
record in the merged PR's comment, so there is no run id to look up.

```yaml
name: apply

on:
  push:
    branches: [main]

concurrency: apply-infra-prod # one apply at a time

jobs:
  apply:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write # the apply-result comment
      actions: read        # cross-run artifact download
    env:
      EXAMPLE_PROVIDER_TOKEN: ${{ secrets.EXAMPLE_PROVIDER_TOKEN }}
    steps:
      - uses: actions/checkout@v6
      - uses: aloutfi/actions/apply@v1
        with:
          stack: infra/
          workspace: prod
          use_planfile: "true"
```

How apply chooses what to run:

| Case | Result |
|---|---|
| A newer commit on the branch changed the stack | Clean skip: the newer run applies |
| `use_planfile: false` | Fresh plan, `-auto-approve` |
| First attempt, reviewed planfile downloaded | Apply the planfile |
| First attempt, no reviewed planfile | **Fail**, or a fresh plan with `allow_fresh_plan_fallback: true` |
| Re-run (attempt > 1) | Fresh plan |

### Skip applies when the PR's plan had no changes ⏭️

Most merges change nothing in most stacks. Gate in a cheap job that holds
only the GitHub token, and skip the apply job, its runner minutes and its
cloud credentials, when the PR's reviewed plan was a no-op:

```yaml
jobs:
  gate:
    runs-on: ubuntu-latest
    permissions: { contents: read, pull-requests: read }
    outputs:
      apply: ${{ steps.gate.outputs.apply }}
    steps:
      - uses: actions/checkout@v6
      - id: gate
        uses: aloutfi/actions/gate@v1
        with:
          stack: infra/
          workspace: prod
          skip_noop: "true"

  apply:
    needs: gate
    if: needs.gate.outputs.apply == 'true'
    # ... the apply job above
```

`skip_noop` skips only when **all** of these hold. Any doubt applies, and the
`reason` output says which check failed:

*   it is the run's first attempt;
*   exactly one PR merged the commit;
*   that PR has exactly one plan record for this stack, written by tfpr's own
    identity, and for the PR's final head;
*   that plan was a no-op;
*   the merged commit's git tree is the tree that was planned.

A stack that reads another stack's state (`terraform_remote_state`) can plan
"No changes" on the PR and differ after the other stack applies in the same
merge. Leave `skip_noop` off for such stacks, or add your own condition to
the apply job's `if:`.

### When the reviewed plan is stale 🔁

`use_planfile` applies only a plan reviewed for the PR's final head. Two
things make it stale:

*   **A push after the last completed plan.** If the PR was merged while the
    newest push's plan was still running, or after it was cancelled, the
    newest record is for an older push. There is no reviewed planfile, and
    the apply fails closed.
*   **State that moved.** If something else applied the stack after the PR
    was planned, terraform refuses the planfile ("Saved plan is stale").

To apply anyway, **re-run the failed apply job**. A re-run never uses the
stored planfile: it plans fresh at the commit and applies, as long as no
newer commit has changed the stack. To fall back to a fresh plan without a
re-run when the reviewed planfile is missing, set
`allow_fresh_plan_fallback: "true"`:

```yaml
      - uses: aloutfi/actions/apply@v1
        with:
          stack: infra/
          use_planfile: "true"
          allow_fresh_plan_fallback: "true" # no reviewed planfile: plan fresh and apply
```

### Path filters and shared modules 🗂️

Before applying, gate and apply check whether the run's commit is still the
head of its branch. If a newer commit changed the stack, applying this older
tree would roll that change back, so the run skips (`reason: superseded`)
and the newer run applies. If no newer commit touched the stack, the run
applies: with `on.push.paths` or per-stack change detection, the newer run
may never apply this stack at all.

What counts as the stack: its directory, and the local modules it calls
(`source = "./…"` or `"../…"`, followed through modules that call others).
List anything else it reads in `paths`, relative to the repository root:
var files, templates, scripts. `.` makes any change in the repository count.

```yaml
      - uses: aloutfi/actions/apply@v1
        with:
          stack: infra/prod
          paths: |
            config/prod.tfvars
```

An entry that isn't a plain repo-relative path (absolute, `..`, a glob, a
backslash) fails the step. If the gate can't read what changed, from git or
from the repo host, it skips: the old behavior.

On a tag or other non-branch ref, the first attempt applies with a warning
and re-runs are refused.

### On [`schedule`](https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows#schedule): drift ⏰

`mode: drift` touches no PR. It writes the full plan to the job summary, sets
`drifted` and warns with the stack's name if anything changed outside
terraform.

```yaml
on:
  schedule: [{ cron: "0 6 * * *" }]

jobs:
  drift:
    runs-on: ubuntu-latest
    strategy:
      matrix: { stack: [infra/net, infra/app] }
    env:
      EXAMPLE_PROVIDER_TOKEN: ${{ secrets.EXAMPLE_PROVIDER_TOKEN }}
    steps:
      - uses: actions/checkout@v6
      - uses: aloutfi/actions/plan@v1
        with:
          stack: ${{ matrix.stack }}
          mode: drift
          fail_on_drift: "true" # optional: fail the job when the stack drifted
```

A plan that failed or didn't run makes `drift_check` `incomplete`, which is
not drift: `drifted` is empty, and `fail_on_drift` fails the job with that
reason.

### Azure Pipelines and your own terraform 🔷

*   [Azure Pipelines and Azure Repos](docs/azure-devops.md): install `tfpr`
    from a release with a pinned checksum, and run the same PR, main-branch
    and drift flows.
*   [Bring your own plan](docs/bring-your-own-plan.md): run terraform
    yourself, with your own wrapper and scrubber, and give tfpr `plan.json`
    and `plan.txt`. The plan step and the comment step can then hold
    different credentials.

## Inputs ⚙️

### plan

```yaml
- uses: aloutfi/actions/plan@v1
  with:
    # Terraform working directory. Cleaned before use, so `infra/`,
    # `./infra` and `infra` are the same stack everywhere.
    # Default: "terraform/"
    stack: ""

    # Terraform workspace, selected with `workspace select -or-create`.
    # Default: "default"
    workspace: ""

    # Terraform version, 1.15.9 or later.
    # Default: "1.17.1"
    terraform_version: ""

    # Upload `tfplan` and `plan-metadata.json` as the artifact
    # `tfplan-<stack-slug>-<workspace>`, for `use_planfile` at merge.
    # Default: "false"
    output_planfile: ""

    # Days to keep the planfile artifact. Apply fails if it has expired by
    # the time the PR merges.
    # Default: "7"
    retention_days: ""

    # Upload the filtered plan text as `<plan_artifact_name>.txt` (not
    # zipped) and link it from the comment.
    # Default: "true"
    upload_plan_text: ""

    # Write the full filtered plan to the job summary and link it from the
    # comment.
    # Default: "true"
    job_summary: ""

    # A comment template file whose {{define}} blocks replace the default's.
    # See docs/plan-comment.md.
    template: ""

    # "pr", or "drift" for a scheduled drift check that touches no PR.
    # Default: "pr"
    mode: ""

    # In drift mode, fail the job when the stack drifted.
    # Default: "false"
    fail_on_drift: ""

    # Only comments by this login are updated. Set it to your App's bot
    # login when `token` is a GitHub App token.
    # Default: "github-actions[bot]"
    comment_author: ""

    # Token for GitHub API calls. Not exposed to terraform.
    # Default: ${{ github.token }}
    token: ""
```

### apply

```yaml
- uses: aloutfi/actions/apply@v1
  with:
    # Same as plan.
    stack: ""
    workspace: ""
    terraform_version: ""

    # Apply the planfile the plan action uploaded for the merged PR's final
    # head. Re-runs always plan fresh.
    # Default: "false"
    use_planfile: ""

    # With use_planfile, plan fresh instead of failing when there is no
    # reviewed planfile on the first attempt.
    # Default: "false"
    allow_fresh_plan_fallback: ""

    # Files or directories outside the stack and its local modules whose
    # changes also supersede an older commit, relative to the repository
    # root. One per line.
    paths: ""

    # Skip when the PR's reviewed plan was a no-op (see gate).
    # Default: "false"
    skip_noop: ""

    # terraform apply -lock-timeout.
    # Default: "5m"
    lock_timeout: ""

    # Same as plan.
    comment_author: ""
    token: ""
```

### gate

The decision of `apply` alone, with only the GitHub token. It writes
`decision.json` to the stack's plan dir, a stable contract
(`tfpr/decision@1`; see [docs/azure-devops.md](docs/azure-devops.md)).

```yaml
- uses: aloutfi/actions/gate@v1
  with:
    # Same as apply.
    stack: ""
    workspace: ""
    paths: ""
    skip_noop: ""
    comment_author: ""
    token: ""
```

### report

Posts the result of a `terraform apply` you ran yourself on the merged PR.
Run it with `if: ${{ !cancelled() }}`. If a gate in the same job skipped or
refused the apply, it reports that and needs no exit code.

```yaml
- uses: aloutfi/actions/report@v1
  if: ${{ !cancelled() }}
  with:
    # Same as plan.
    stack: ""
    workspace: ""

    # Your terraform apply's exit code.
    apply_exit_code: ""

    # Path to your apply's output, already scrubbed. It is filtered and cut
    # to fit, keeping the end.
    apply_log: ""

    # Same as plan.
    comment_author: ""
    token: ""
```

## Outputs 📤

### plan

| Output | Description |
|---|---|
| `exit_code` | `terraform plan -detailed-exitcode`: 0 no changes, 2 changes |
| `has_changes` | `true` if the plan changes anything, outputs included |
| `has_imports` | `true` if the plan imports anything |
| `has_output_changes` | `true` if any output changes |
| `summary` | `N to add, N to change, N to destroy, N to replace, N to import` |
| `plan_artifact_name` | The planfile artifact's name (with `output_planfile`) |
| `plan_text_url` | Download URL of the `plan.txt` artifact |
| `drifted` | Drift mode: `true` if the stack drifted; empty if the check was incomplete |
| `drift_check` | Drift mode: `complete` or `incomplete` |

### apply

| Output | Description |
|---|---|
| `action` | `apply`, `skip` or `refuse` |
| `apply_exit_code` | terraform apply's exit code; empty when skipped |
| `used_plan_artifact` | `true` if the reviewed planfile was applied |
| `fallback_reason` | `rerun`, `missing-artifact`, `no-artifact-requested`, `superseded`, `no-op`, `unverified-ref`, or empty |

### gate

| Output | Description |
|---|---|
| `apply` | `true` to apply, `false` to skip; use it in the apply job's `if:` |
| `action` | `apply`, `skip` or `refuse` |
| `reason` | Why: `superseded`, `no-op`, `unverified-ref`, or why a no-op couldn't be skipped |
| `pr_number` | The PR that merged the commit, or empty |
| `plan_run_id` | The run that planned that PR's final head, or empty |

## The plan comment 💬

```markdown
### ⚠️ Terraform plan · `infra` (`prod`) · 3,000 to add, 40 to change, 25 to destroy, 5 to replace
Commit `7c79a8b` · [job log](…) · [full plan](…) · [plan.txt](…) · [tfpr v1.0.0](…) · [terraform 1.17.1](…) · redaction: none

> **Plan text truncated**: showing 42,748 of 600,946 characters; the full plan is linked above.
> **Resource list shortened**: every delete, replace and forget is listed; 2,556 lower-risk lines are counted, not listed.

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 3,000 | 40 | 25 | 5 | 0 | 0 | 0 | 1 | 0 |
```

Then the risk-ordered resource list, the plan text (collapsed) and the
outputs that change. Outside a pull request, the plan action writes only the
job summary. [docs/plan-comment.md](docs/plan-comment.md) covers the layout,
how it stays under the host's size limit, and changing it with a template.

## Credentials 🔑

*   Provider credentials: job-level `env:`.
*   The `token` input is used only for GitHub API calls. tfpr gets it as
    `TFPR_GITHUB_TOKEN` and removes every `TFPR_*` variable from terraform's
    environment. A `GITHUB_TOKEN` you set in the job's `env:` for the GitHub
    provider still reaches terraform.
*   With a GitHub App token, set `comment_author` to the App's bot login
    (`my-app[bot]`) so tfpr finds its own comments.
*   Private module sources over SSH: start an agent in an earlier step, such
    as `webfactory/ssh-agent`.
*   Redaction: set `TFPR_FILTER` to a command that scrubs stdin to stdout. A
    failing filter posts nothing. `plan.json` is never published. See
    [docs/redaction.md](docs/redaction.md).

## Terraform versions 🧱

Terraform 1.15.9 and later are supported, and the actions default to 1.17.1.
CI plans the test stacks with 1.15.9, the newest 1.16 and the newest 1.17,
and every one must render the same comments.

## Versioning 🏷️

Releases follow [Semantic Versioning](https://semver.org): a breaking change to
inputs, outputs, the comment or `decision.json` is a new major version. Pin
the depth you want:

| Pin | Moves to | Example |
|---|---|---|
| `@v1` | every 1.x release: fixes and features | `aloutfi/actions/plan@v1` |
| `@v1.0` | every 1.0.x release: fixes only | `aloutfi/actions/plan@v1.0` |
| `@v1.0.0` or a commit SHA | nothing | `aloutfi/actions/plan@v1.0.0` |

Each release commit carries `checksums.txt` for its binaries, so whatever the
ref resolves to pins the `tfpr` that runs. A release is cut by changing
`VERSION` on `main`; the release workflow tags `v<version>` and moves
`v<major>` and `v<major>.<minor>` to it (prereleases move neither).

How the actions get `tfpr`, first match wins:

1.  `TFPR_BIN` in the job's `env:`: a path to a prebuilt binary.
2.  A release ref: download `tfpr_<version>_<os>_<arch>.tar.gz` from this
    repo's release and check it against `checksums.txt` at that ref.
3.  Any other ref: build from the action's own source with
    `actions/setup-go`.

## Examples 📄

<details>
<summary>Plan several stacks with a matrix</summary>

```yaml
jobs:
  plan:
    runs-on: ubuntu-latest
    permissions: { contents: read, pull-requests: write, actions: read }
    strategy:
      fail-fast: false
      matrix:
        include:
          - { stack: infra/net, workspace: default }
          - { stack: infra/app, workspace: staging }
          - { stack: infra/app, workspace: prod }
    steps:
      - uses: actions/checkout@v6
      - uses: aloutfi/actions/plan@v1
        with:
          stack: ${{ matrix.stack }}
          workspace: ${{ matrix.workspace }}
```

Each stack and workspace gets its own comment.

</details>

<details>
<summary>Gate, apply your own way, then report</summary>

```yaml
jobs:
  apply:
    runs-on: ubuntu-latest
    permissions: { contents: read, pull-requests: write }
    steps:
      - uses: actions/checkout@v6
      - id: gate
        uses: aloutfi/actions/gate@v1
        with: { stack: infra/, skip_noop: "true" }

      - id: apply
        if: steps.gate.outputs.apply == 'true'
        run: |
          set -o pipefail
          code=0
          ./scripts/apply.sh infra/ 2>&1 | tee "${RUNNER_TEMP}/apply.log" || code=$?
          echo "exit_code=${code}" >> "${GITHUB_OUTPUT}"
          exit "${code}"

      - uses: aloutfi/actions/report@v1
        if: ${{ !cancelled() }}
        with:
          stack: infra/
          apply_exit_code: ${{ steps.apply.outputs.exit_code }}
          apply_log: ${{ runner.temp }}/apply.log
```

</details>

<details>
<summary>Comment as a GitHub App</summary>

```yaml
      - id: app
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.APP_CLIENT_ID }}
          private-key: ${{ secrets.APP_PRIVATE_KEY }}
      - uses: aloutfi/actions/plan@v1
        with:
          stack: infra/
          token: ${{ steps.app.outputs.token }}
          comment_author: my-app[bot]
```

</details>

<details>
<summary>Give terraform a GitHub token for the GitHub provider</summary>

`token` never reaches terraform. Export the token terraform should use:

```yaml
      - run: echo "GITHUB_TOKEN=${APP_TOKEN}" >> "${GITHUB_ENV}"
        env:
          APP_TOKEN: ${{ steps.app.outputs.token }}
      - uses: aloutfi/actions/plan@v1
        with:
          stack: infra/github
```

</details>

<details>
<summary>Private modules over SSH</summary>

```yaml
      - uses: webfactory/ssh-agent@v0.9.0
        with:
          ssh-private-key: ${{ secrets.MODULES_DEPLOY_KEY }}
      - uses: aloutfi/actions/plan@v1
        with:
          stack: infra/
```

The agent's socket is exported to `$GITHUB_ENV`, so terraform sees it.

</details>

<details>
<summary>Add owners and a runbook link to every comment</summary>

`.github/tfpr-comment.tmpl`:

```gotemplate
{{define "footer"}}
Owners: @infra-team · [Runbook](https://example.invalid/runbook)
{{end}}
```

```yaml
      - uses: aloutfi/actions/plan@v1
        with:
          stack: infra/
          template: .github/tfpr-comment.tmpl
```

</details>

<details>
<summary>Redact values before they reach the comment</summary>

```yaml
    env:
      TFPR_FILTER: >-
        sed -E -e 's/(AKIA[0-9A-Z]{16})/[redacted]/g'
        -e 's/(password *= *).*/\1[redacted]/'
    steps:
      - uses: actions/checkout@v6
      - uses: aloutfi/actions/plan@v1
        with:
          stack: infra/
```

The filter runs over terraform's output, the stored plan text, the resource
names copied from `plan.json` and every comment body. See
[docs/redaction.md](docs/redaction.md).

</details>

<details>
<summary>Act on drift in each stack</summary>

```yaml
jobs:
  drift:
    strategy:
      fail-fast: false
      matrix: { stack: [infra/net, infra/app] }
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - id: plan
        uses: aloutfi/actions/plan@v1
        with: { stack: "${{ matrix.stack }}", mode: drift }
      - if: steps.plan.outputs.drifted == 'true'
        env: { STACK: "${{ matrix.stack }}" }
        run: ./scripts/notify-drift.sh "${STACK}"
```

</details>

## Development 🛠️

```sh
go test ./...                # unit tests, render goldens, command scenarios
test/run.sh                  # black-box CLI suite (builds tfpr; or set TFPR_BIN)
test/run.sh --update         # rewrite golden files
test/run.sh --compat         # goldens with resource ids and hashes masked
go test ./cmd/tfpr -update   # rewrite the comment goldens the Go tests own
test/fixtures/generate.sh [case...]  # re-plan fixtures with the terraform on PATH
gofmt -l . && go vet ./...
shellcheck scripts/install-tfpr.sh test/run.sh test/fixtures/generate.sh
actionlint
```

`core/` is pure: the PlanResult and its derivation, naming, apply policies
and the comment templates. `ci/` holds the CI adapters (`CI` in
[ci/ci.go](ci/ci.go)), `vcs/` the repo-host adapters (`VCS` in
[vcs/vcs.go](vcs/vcs.go)), `redact/` runs `TFPR_FILTER`, and `cmd/tfpr`
wires them together. A comment body is a `redact.Text`, which only the
configured filter can produce. `test/cases/*.tsv` and the golden files under
`test/fixtures/` are the shared behavior spec. The terraform executable is
`TFPR_TF_BIN` (default `terraform`).

## License

[MIT](LICENSE)
