# Azure DevOps

tfpr runs in Azure Pipelines and comments on Azure Repos pull requests. It
detects Azure Pipelines from `TF_BUILD=True`. The repo host follows
`Build.Repository.Provider`:

- Azure Repos (`TfsGit`): one PR thread per stack, through the Azure Repos
  REST API.
- GitHub: comments on the GitHub PR. This is the same code GitHub Actions
  uses; set `TFPR_GITHUB_TOKEN` instead of `TFPR_ADO_TOKEN`. For GitHub
  Enterprise, tfpr uses the API on the repository's host
  (`https://<host>/api/v3`, taken from `Build.Repository.Uri`) or
  `TFPR_GITHUB_API_URL`. It never falls back to api.github.com, which would
  send your enterprise token there: if neither is known, it fails.
- On a GitHub repo, tfpr edits only comments by its own login. Outside GitHub
  Actions it asks GitHub whose token it holds (`GET /user`), which works for
  personal access tokens. GitHub App installation tokens can't answer that,
  so set `TFPR_COMMENT_AUTHOR` to the app's bot login (`<app-slug>[bot]`).
  If the author is unknown, `comment` fails and posts nothing. Posting
  anyway would add a new comment on every run.

This page covers three flows: the PR flow (plan, then comment), the apply on
the main branch (gate, your apply, report), and scheduled drift detection.

## Install a pinned release

Pin both the version and the sha256 in your own pipeline. Never fetch the
checksum from the same release: the sha256 in your YAML is the trust root,
and the release notes only repeat it for convenience.

```yaml
- bash: |
    set -euo pipefail
    v=<version>                                # the release you reviewed, e.g. 1.2.3 (no "v")
    sha=<sha256 of tfpr_<version>_linux_amd64.tar.gz>
    f="$(Agent.TempDirectory)/tfpr.tgz"
    curl -fsSLo "$f" "https://github.com/aloutfi/actions/releases/download/v${v}/tfpr_${v}_linux_amd64.tar.gz"
    echo "${sha}  ${f}" | sha256sum -c -
    mkdir -p "$(Agent.ToolsDirectory)/tfpr"
    tar -xzf "$f" -C "$(Agent.ToolsDirectory)/tfpr" tfpr
    echo "##vso[task.prependpath]$(Agent.ToolsDirectory)/tfpr"
  displayName: Install tfpr
```

Windows agents (PowerShell):

```yaml
- pwsh: |
    $v = '<version>'
    $sha = '<sha256 of tfpr_<version>_windows_amd64.tar.gz>'
    $f = Join-Path $env:AGENT_TEMPDIRECTORY 'tfpr.tgz'
    Invoke-WebRequest -Uri "https://github.com/aloutfi/actions/releases/download/v$v/tfpr_${v}_windows_amd64.tar.gz" -OutFile $f
    if ((Get-FileHash $f -Algorithm SHA256).Hash.ToLower() -ne $sha) { throw 'tfpr: checksum mismatch' }
    $d = Join-Path $env:AGENT_TOOLSDIRECTORY 'tfpr'
    New-Item -ItemType Directory -Force $d | Out-Null
    tar -xzf $f -C $d
    Write-Host "##vso[task.prependpath]$d"
  displayName: Install tfpr
```

Hosted Ubuntu agents have bash, curl, `sha256sum` and tar. On other agents,
check that they're present. `TFPR_FILTER` runs with `sh -c`, so on Windows
it needs `sh` on `PATH` (Git for Windows provides one).

## The PR flow: plan with cloud credentials, comment with only the PR token

You run terraform yourself, inside your cloud-auth task and through your own
scrubber. tfpr runs only in a later step that holds nothing but the
pipeline's access token.

```yaml
steps:
- checkout: self

# 1. Install tfpr (above).

# 2. Plan. Cloud credentials only; tfpr is not involved.
- task: AzureCLI@2                           # or whichever task holds your cloud credentials
  displayName: Terraform plan
  inputs:
    azureSubscription: example-connection
    scriptType: bash
    scriptLocation: inlineScript
    inlineScript: |
      set -euo pipefail
      out="$(Agent.TempDirectory)"
      rm -f "$out/plan.json" "$out/plan.txt" "$out/plan.exitcode"
      terraform -chdir=infra init -input=false
      code=0
      terraform -chdir=infra plan -input=false -no-color -detailed-exitcode -out=tfplan || code=$?
      echo "$code" > "$out/plan.exitcode"
      [ "$code" -ne 1 ] || exit 1
      terraform -chdir=infra show -json tfplan > "$out/plan.json"             # stdout only: never 2>&1
      terraform -chdir=infra show -no-color tfplan | ./scrub > "$out/plan.txt"

# 3. Comment. Only the access token. Runs when the plan failed too (so the
#    thread says so), but not when the run was cancelled (a newer run may
#    already have posted a good plan).
- bash: |
    failed=0
    TFPR_EXIT_CODE="$(cat "$(Agent.TempDirectory)/plan.exitcode" 2>/dev/null)" tfpr derive || failed=1
    tfpr comment || failed=1
    exit "$failed"
  displayName: Plan comment
  condition: not(canceled())
  env:
    TFPR_STACK: infra
    TFPR_FILTER: ./scrub                     # also filters the comment body and the names from plan.json
    TFPR_PLAN_JSON: $(Agent.TempDirectory)/plan.json
    TFPR_PLAN_TXT: $(Agent.TempDirectory)/plan.txt
    TFPR_ADO_TOKEN: $(System.AccessToken)    # secrets reach a step only through env
```

- `derive` and `comment` are not chained with `&&`. A failed plan makes
  `derive` exit non-zero after it records the failure, and `comment` must
  still run so the thread says "Plan failed" instead of showing the last good
  plan. The step still fails.
- Map the token to `TFPR_ADO_TOKEN`. tfpr strips every `TFPR_*` variable from
  the environment of anything it runs (terraform, the filter), so the token
  never reaches them. Don't expose it as `SYSTEM_ACCESSTOKEN` in a step that
  runs terraform.
- `plan.json` holds values in clear text. Keep it on the agent; never publish
  it as an artifact.
- tfpr's outputs are output variables (`isOutput=true`). Give the step a
  `name:` to read them in later jobs, e.g. `$[ dependencies.plan.outputs['tfpr.has_changes'] ]`.

## The main-branch flow: gate, your apply, report

On a merge to the main branch you apply with a fresh plan at the merge
commit, inside your cloud-auth task and through your scrubber. tfpr runs
only before and after it, with only the access token:

- `tfpr gate` decides whether to apply. A commit that is no longer the head
  of its branch is a clean skip (`apply` is `false`, a warning, exit 0): the
  run for the newer head applies the newer tree, which includes this
  commit's changes. If tfpr can't verify the branch head, the step fails.
  Opt-in `TFPR_SKIP_NOOP=true` also skips when the merged PR's reviewed plan
  was a no-op and the merged tree is the tree that was planned; any doubt
  applies.
- `tfpr report` posts the result on the merged PR's thread for the stack.
  A successful apply or a skip closes the thread, and a failed apply leaves
  it active. A superseded run says "Superseded by `<sha>`" and links the
  build that applies instead.

```yaml
trigger:
  branches: { include: [main] }
pr: none
lockBehavior: sequential   # with an Exclusive Lock check on the apply service connection

steps:
- checkout: self

# 1. Install tfpr (above).

# 2. Gate. Only the access token; no cloud credentials.
- bash: tfpr gate
  name: gate
  displayName: Gate
  env:
    TFPR_STACK: infra
    TFPR_ADO_TOKEN: $(System.AccessToken)
    # TFPR_SKIP_NOOP: "true"                 # opt-in: skip a merge whose PR plan was a no-op

# 3. Apply. Cloud credentials only, and only when the gate says so.
- task: AzureCLI@2
  displayName: Terraform apply
  condition: and(succeeded(), eq(variables['gate.apply'], 'true'))
  inputs:
    azureSubscription: example-connection
    scriptType: bash
    scriptLocation: inlineScript
    inlineScript: |
      set -uo pipefail
      terraform -chdir=infra init -input=false
      terraform -chdir=infra apply -input=false -no-color -auto-approve 2>&1 \
        | ./scrub | tee "$(Agent.TempDirectory)/apply.log"
      code=${PIPESTATUS[0]}
      echo "##vso[task.setvariable variable=TFPR_APPLY_EXIT_CODE]$code"
      exit "$code"

# 4. Report. Only the access token. Always, so a failed or refused apply is
#    reported too.
- bash: tfpr report
  displayName: Apply report
  condition: always()
  env:
    TFPR_STACK: infra
    TFPR_FILTER: ./scrub
    TFPR_APPLY_LOG: $(Agent.TempDirectory)/apply.log
    TFPR_ADO_TOKEN: $(System.AccessToken)
```

- **Run applies one at a time.** Set `lockBehavior: sequential` and add an
  **Exclusive Lock** check to the service connection the apply uses. Queued
  runs then wait their turn, and when a burst of merges lands, every run but
  the newest reaches the gate superseded and skips. The newest applies
  everything.
- The gate writes `decision.json` to the stack's plan dir (`infra/.tfpr`).
  `report` reads it, so a skip or refusal needs no exit code.
  `TFPR_APPLY_EXIT_CODE`, set by the apply step as a pipeline variable,
  reaches report as an environment variable.
- `report` finds the merged PR from the merge commit: `pullrequestquery`,
  or the `Merged PR <id>:` title of a squash merge, accepted only if that PR
  really merged this commit into this branch. Without a PR it just logs.

## Drift detection on a schedule

The same plan as the PR flow, on a schedule, with `TFPR_MODE=drift`. No PR is
touched, and no repo token is needed:

```yaml
schedules:
- cron: "0 6 * * *"
  branches: { include: [main] }
  always: true
trigger: none
pr: none

jobs:
- job: drift
  strategy:
    matrix:
      net: { STACK: infra/net }
      app: { STACK: infra/app }
  steps:
  - checkout: self
  # 1. Install tfpr (above).
  # 2. Plan $(STACK) inside your cloud-auth task, as in the PR flow.
  # 3. Report drift:
  - bash: |
      TFPR_EXIT_CODE="$(cat "$(Agent.TempDirectory)/plan.exitcode" 2>/dev/null)" tfpr derive && tfpr comment
    name: drift
    displayName: Drift report
    condition: not(canceled())
    env:
      TFPR_MODE: drift
      TFPR_STACK: $(STACK)
      TFPR_FILTER: ./scrub
      TFPR_PLAN_JSON: $(Agent.TempDirectory)/plan.json
      TFPR_PLAN_TXT: $(Agent.TempDirectory)/plan.txt
      # TFPR_FAIL_ON_DRIFT: "true"           # end the step SucceededWithIssues on drift
```

For each stack, `comment`:

- uploads the full, filtered plan as the run's summary;
- sets the output variable `has_changes`;
- on drift, logs a warning naming the stack and tags the run `drift`.

With `TFPR_FAIL_ON_DRIFT=true`, a drifted stack ends its step
**SucceededWithIssues**. `has_changes` is per stack. To act on any drift, use
the `drift` build tag (for example in a notification subscription), or
combine the matrix legs' outputs in a later job:
`dependencies.drift.outputs['net.drift.has_changes']`.

## Permissions and policies

- The build identity (the project's or the collection's build service, which
  tfpr finds through `connectionData`) needs **Contribute to pull requests**
  on the repository, for the PR comment and the apply report. The gate
  reads the branch and the merged PR, which needs only read access.
- Run the pipeline on PRs with a **Build validation** branch policy. Azure
  Repos ignores `pr:` triggers in YAML.

## Threads

- One thread per stack and workspace. tfpr finds its thread by the thread's
  properties (`tfpr.marker`) and updates it only if the build identity wrote
  its first comment. A reviewer can't hijack it by pasting a marker.
- The properties also carry the record (`tfpr.record`) and the plan
  fingerprint, so the comment text has no hidden markup.
- Status: a no-op plan's thread is **closed**; any other plan's is
  **active**. With the **Check for comment resolution** policy, reviewers must
  resolve plan threads before the PR completes.
- `TFPR_ACK=true` ("plan was read"): when a re-run plans the same thing, tfpr
  leaves a reviewer's resolution alone. When the plan changes, it reopens the
  thread with a reply saying so. Without it, every run sets the status again.

## The full plan and the comment layout

- `tfpr comment` uploads the full, filtered plan as the run's summary
  (`task.uploadsummary`), on the run's **Extensions** tab. That tab has no
  stable URL of its own, so the comment's "full plan" link opens the run.
- Until a real PR shows what Azure Repos renders, the comment uses a
  conservative dialect: headings instead of collapsible `<details>` blocks,
  plain code fences instead of ```` ```diff ```` (each resource line still
  says its action), and at most 140,000 characters.

## Not yet verified on a real PR

These are assumptions until checked on a test PR:

- Whether `<details>` and `<summary>` render in PR threads, and whether a
  ```` ```diff ```` fence is colored. If they do, the dialect can switch to
  them.
- The comment size limit, reportedly about 150,000 characters. tfpr stays
  under 140,000.
- The size limit of `task.uploadsummary`, which is undocumented. tfpr keeps
  the summary under 512 KiB and falls back to a minimal summary past that.
- Which commits a PR build sees. tfpr records the checked-out commit,
  `Build.SourceVersion` (the merge commit), as `source.sha`, and the PR head,
  `System.PullRequest.SourceCommitId`, as `source.head_sha`. This matches
  GitHub Actions, where `GITHUB_SHA` is the merge commit. It logs both.

## Why no step template

A template in this repository can only be referenced through a
`resources.repositories` entry with a GitHub service connection. The
snippets above need none, so they're the supported path.
