# Azure DevOps

tfpr runs in Azure Pipelines and comments on Azure Repos pull requests. It
detects Azure Pipelines from `TF_BUILD=True`. The repo host follows
`Build.Repository.Provider`:

- Azure Repos (`TfsGit`): one PR thread per stack, through the Azure Repos
  REST API.
- GitHub: comments on the GitHub PR. This is the same code GitHub Actions
  uses; set `TFPR_GITHUB_TOKEN` instead of `TFPR_ADO_TOKEN`.

This page covers the PR flow: plan, then comment. Apply on the main branch
(`gate` and `report`) and scheduled drift detection come next.

## Install a pinned release

Pin both the version and the sha256 in your own pipeline. Never fetch the
checksum from the same release: the sha256 in your YAML is the trust root,
and the release notes only repeat it for convenience.

```yaml
- bash: |
    set -euo pipefail
    v=0.2.0                                    # the release you reviewed
    sha=<sha256 of tfpr_0.2.0_linux_amd64.tar.gz>
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
    $v = '0.2.0'
    $sha = '<sha256 of tfpr_0.2.0_windows_amd64.tar.gz>'
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

## Permissions and policies

- The build identity (the project's or the collection's build service, which
  tfpr finds through `connectionData`) needs **Contribute to pull requests**
  on the repository.
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

## Not yet verified on a real PR

These are assumptions until checked on a test PR. Check them before you rely
on the comment layout:

- Whether `<details>` and `<summary>` render in PR threads. The current
  layout uses them; a host-specific layout is planned.
- Whether a ```` ```diff ```` fence is colored.
- The comment size limit, reportedly about 150,000 characters. The default
  `TFPR_COMMENT_BUDGET` (45,000) stays well under it.
- The size limit of `task.uploadsummary`.
- Which of `System.PullRequest.SourceCommitId` and `Build.SourceVersion` is
  the commit you expect. tfpr plans the first (the PR head) and logs both.

## Why no step template

A template in this repository can only be referenced through a
`resources.repositories` entry with a GitHub service connection. The
snippets above need none, so they're the supported path.
