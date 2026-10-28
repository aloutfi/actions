# Redaction: `TFPR_FILTER`

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

It fails closed. A non-zero exit, empty output for non-empty input, or a
filter that hasn't finished two minutes after its input ended fails the
command, and nothing is posted. For terraform output, which streams through
the filter for as long as terraform runs, the two minutes start when
terraform exits. Filters must be idempotent, because the plan text is filtered once when it is stored and
again inside the comment body. They must also keep one output line per input
line, because names are filtered one per line. The filter does not see tfpr's
own `TFPR_*` variables. `plan-result.json` records whether a filter ran
(`redaction.mode`: `filter` or `none`).

Resource addresses and output names come from `plan.json`, so a scrubber
you run over `plan.txt` never sees them. They reach `plan-result.json`
unfiltered unless `TFPR_FILTER` is set. They are filtered again in any
comment body, but `plan-result.json` itself keeps them as they were. If
for_each keys can be sensitive, set `TFPR_FILTER`, even when you already
scrub `plan.txt` yourself.

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
