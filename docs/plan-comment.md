# The plan comment

tfpr posts one comment per stack and workspace on the pull request, and
updates it on every push. It also writes the full plan to the job summary
and, in the GitHub Action, uploads it as a `plan.txt` artifact. This page
describes the layout, how it stays under the host's size limit, and how to
change it.

## Layout

Anything a reviewer must know comes before anything collapsed. For a plan
too large for one comment, the top of the comment looks like this:

```markdown
### ⚠️ Terraform plan · `large` (`default`) · 3,000 to add, 40 to change, 25 to destroy, 5 to replace
> **Plan text truncated**: showing 42,594 of 600,946 characters.
> **Resource list shortened**: every delete, replace and forget is listed; 2,555 lower-risk lines are counted, not listed.
> Full plan: [job summary](…) · [plan.txt](…) · [job log](…)
> Redaction: no redaction filter configured

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 3,000 | 40 | 25 | 5 | 0 | 0 | 0 | 1 | 0 |

**Resources (3,070)**
```

In order:

1. **Headline**: status icon, stack, workspace and what the plan does.
2. **Banner**, only when something needs attention: the plan text was cut
   ("showing N of M characters"), the resource list was shortened, or
   resources will be forgotten. When there is a banner, it ends with
   the full-plan links and the redaction state.
3. **Counts**, including moves, forgets, output changes and drift.
4. **Resources**: every changed resource from `plan.json`, riskiest first:
   delete, replace, forget, update, import, move, create, read.
5. **Plan output**: terraform's own text, collapsed. Lines like
   `# (2 unchanged attributes hidden)` are left out of the comment
   (`TFPR_KEEP_UNCHANGED=true` keeps them). The job summary and `plan.txt`
   keep them.
6. **Outputs** that change, then a footer with the commit, the job log, the
   full plan, the tfpr version and the redaction mode.

The headline phrase follows the plan's status:

| Status | Headline |
|---|---|
| `no_op` | No changes |
| `changes` | 3 to add, 1 to change, 2 to destroy (plus replace, import, move and forget counts when non-zero) |
| `outputs_only` | No resource changes; 1 output changes. Downstream `terraform_remote_state` readers are affected |
| `state_only` | No infrastructure changes; state-only: 1 moved, 2 forgotten |
| `error` | Plan failed, with the reason and a link to the job log |

## Staying under the limit

GitHub rejects comments over 65,536 characters. tfpr fits the whole comment
(the hidden marker and record lines included) into that limit, or into
`TFPR_COMMENT_BUDGET` characters when you set it:

- The resource list gets at most 30% of the budget. Delete, replace and
  forget lines are never shortened: they are listed in full whenever they
  fit in the comment at all. Other groups end with a count, such as
  `+ 2,555 more create lines`. The list is never cut mid-group without a
  count.
- The plan text gets the rest, cut at a line boundary. tfpr renders the
  comment once without the text to measure everything else, then fits the
  text into what is left, keeping a margin for the redaction filter.
- If the result still does not fit, for example because a custom template
  prints every resource, or the filter made the text much longer, tfpr posts
  a minimal comment instead: headline, banner, counts and links. A comment is
  never cut off mid-HTML.

Counts are characters, not bytes, so non-ASCII text is measured the way
GitHub measures it, and nothing is ever cut inside a UTF-8 sequence.

Plan text is placed in a code fence longer than any run of backticks in it,
so a value containing ```` ``` ```` cannot end the fence. Stack names and
resource addresses are escaped where they land in HTML (`<summary>`) and
wrapped in code spans elsewhere.

## The full plan

- **Job summary.** `tfpr comment` writes the filtered plan, with the
  complete resource list, to the CI's job summary before it posts the comment.
  The comment links to it. On GitHub this is `$GITHUB_STEP_SUMMARY`, limited
  to 1 MiB per step; a larger plan is cut the same way, with a banner. Outside
  a pull request `tfpr comment` writes only the summary. `TFPR_SUMMARY=false`
  (action input `job_summary: "false"`) turns it off.
- **plan.txt.** The plan action uploads the stored, filtered plan text as an
  artifact named `<plan_artifact_name>.txt`, uncompressed
  (`upload-artifact` with `archive: false`), and passes its URL to the
  comment as `TFPR_PLAN_TXT_URL`. Turn it off with `upload_plan_text: "false"`.
  GitHub serves artifact links to signed-in users only.

Both pass through `TFPR_FILTER`, like the comment.

## Changing the comment: `TFPR_TEMPLATE`

The default comment is a Go [`text/template`](https://pkg.go.dev/text/template)
built from named blocks ([core/templates/plan.md.tmpl](../core/templates/plan.md.tmpl)).
Set `TFPR_TEMPLATE` (action input `template`) to a file of your own:

- Each `{{define "name"}}` in your file replaces the default block of that
  name. Blocks you do not define keep their default.
- If your file also has content outside `{{define}}`, that content replaces
  the whole comment. A file with only blocks, white space and comments
  keeps the default document.

To change only the footer:

```gotemplate
{{define "footer"}}
Owners: @infra-team · [Runbook](https://example.invalid/runbook)
{{end}}
```

```yaml
- uses: aloutfi/actions/plan@vX.Y.Z
  with:
    stack: infra/
    template: .github/tfpr-comment.tmpl
```

The blocks are `header`, `banner`, `links`, `counts`, `unit`, `resources`,
`body`, `outputs`, `footer` and `failed` (the body of a failed plan).
`minimal` is the fallback layout and always uses the built-in blocks. Most
blocks get a `PlanView`: `.Result` (the PlanResult), `.Host`, `.Links`
(`.Job`, `.Summary`, `.PlanText`), `.Status`, `.Headline`, `.Counts`,
`.Banner` and `.Units`. `unit`, `resources` and `body` get one
`UnitView` (`.Path`, `.Groups`, `.List`, `.Body`, `.Truncated` and so on).
Both are documented in [core/render.go](../core/render.go).

Templates can call only these functions, besides `text/template`'s
built-ins. None of them can read the environment or files:

| Function | Does |
|---|---|
| `fence LANG TEXT` | A fenced code block that TEXT cannot close |
| `code S` | An inline code span that S cannot close |
| `html S` | Escapes `&`, `<`, `>`, `"` and `'` |
| `plural N WORD` | `1 resource`, `3,000 resources` |
| `details OPEN SUMMARY BODY` | A `<details>` block where the host renders one, else a heading; escapes SUMMARY |
| `short SHA` | The first 7 characters |

Your template is budgeted like the default one. The job summary always
uses the default layout.

## Hosts

Each host's limits and markdown dialect are one row in `hosts` in
[core/host.go](../core/host.go):

```go
{Name: "github", MaxComment: 65536, MaxSummary: 1 << 20, Details: true, DiffFence: true, HiddenHTML: true},
```

`Details: false` turns collapsible blocks into headings, and
`DiffFence: false` puts the resource list in a plain fence; each line keeps
its action word. Supporting a new host means adding a row. Names the table
does not list get the first row.
