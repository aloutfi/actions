# Bring your own plan

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
# Run it even when step 1 failed, so a failed plan replaces the last good one
# on the PR, but not when the run was cancelled: a cancelled run may have been
# superseded by a newer, good plan. GitHub Actions: if: ${{ !cancelled() }}.
# Azure Pipelines: condition: not(canceled()).
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
  action's comment step runs unless the run was cancelled (`!cancelled()`).
  Use the same condition for your own comment step (`not(canceled())` on
  Azure Pipelines). Never use "always", which also posts "plan failed" from
  a cancelled run over a newer good plan.
- If the two steps run in different jobs, run `derive` in step 1 and hand over
  only the plan dir (below), never `plan.json`.
- `derive` and `render` work anywhere. `comment` needs a pull request from a
  supported CI (GitHub Actions, Azure Pipelines) on a supported repo host
  (GitHub, Azure Repos). For Azure DevOps, see
  [azure-devops.md](azure-devops.md).

## The plan dir

`tfpr plan` and `tfpr derive` write the plan dir, `$TFPR_DIR` (default
`<stack>/.tfpr`). `render` and `comment` read only this directory, so they
need no terraform and no cloud credentials:

```
plan-result.json        what was planned: units, counts, statuses, planfile hash; no values
units/<path>/plan.txt   each unit's plan text, already filtered (plain terraform: units/plan.txt)
```

`plan-result.json` follows [schema/plan-result.v1.json](../schema/plan-result.v1.json)
(`tfpr/plan-result@1`). Plain terraform is one unit at path `.`. The PR
comment carries a one-line hidden record of it (`<!-- tfpr:plan@1 {...} -->`,
right after the comment's marker line): the key, the commit and tree, the
status, the units that still need an apply, and the planfile hash. A later
merge-time stage reads it back from the bot's own comment.
