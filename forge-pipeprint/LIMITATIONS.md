# Limitations

What the first version of forge-pipeprint leaves out, mostly because it is
complex to do right. Most unmodeled settings are still kept in `native`
blocks, so they survive a round trip to the same forge. They are only
dropped, with a warning, when compiling to another forge.

## Expressions

- Expressions are not translated. `${{ github.ref }}` and
  `$CI_COMMIT_BRANCH` stay as written. Job conditions are kept per dialect;
  a job without an expression for the target runs unconditionally, with a
  warning.
- Commands that use `${{ }}` or GitLab `CI_` variables are copied as-is,
  with a warning when compiled to the other dialect.
- Matrix values are not rewritten from `matrix.x` to `$X` or back.

## Files

- YAML comments, anchors and key order are not kept. Keys are written in a
  fixed order; matrix axes and native settings are sorted.
- One file in, one file out. Includes, reusable workflows and local or
  composite actions are not resolved or inlined.
- There is no JSON Schema for Pipeprint yet.

## GitHub and Forgejo

- Forgejo is treated as the GitHub dialect. Forgejo-specific differences,
  such as unsupported keys, runner labels and action URLs, are not checked.
- Native only: reusable workflow calls (`workflow_call`, job `uses`),
  `permissions`, `concurrency`, `defaults`, job `outputs`, `strategy`
  options and matrix `include`/`exclude`, container options, and step
  `id`, `env`, `if`, `shell` and `working-directory`.
- Event filters other than `branches`, `tags` and `paths`, such as
  `branches-ignore` and pull request `types`, are native only.
- `workflow_dispatch` inputs are native only; they are not mapped to GitLab
  pipeline inputs or variables.

## GitLab

- Not resolved, kept native: `include`, `extends`, hidden jobs, `default`,
  `artifacts`, `cache`, `retry`, `when`, `trigger` and child pipelines.
- YAML tags such as `!reference` are not kept: they are reported, and the
  value is written back as a plain list. Resolve them before converting.
- `rules` are modeled only as one plain `if`. Workflow rules are read only
  in the forms Pipeprint writes; others stay native and the triggers are
  assumed to be push.
- Files with a `spec:` inputs header are rejected.
- `needs` options such as `artifacts` and `optional` are dropped, with a
  warning. Needs on jobs outside the file are dropped too.
- `before_script` is merged into `script`.
- Variables with descriptions or options stay native as a whole.
- Schedules are set in project settings, not in the file, so crons are
  dropped when writing GitLab and empty when reading it.

## Across forges

- Actions (`uses`) cannot run on GitLab and are dropped. Checkout is
  implicit there.
- GitLab has no per-step environment, conditions or working directory.
- GitHub-hosted runner labels, such as `ubuntu-latest`, are dropped for
  GitLab. Jobs without runner labels get `ubuntu-latest` on GitHub.
- GitHub runs each step in a new shell; GitLab runs all script lines in
  one. A `cd` in one GitHub step affects later lines on GitLab.
- Branch globs with `[]`, `!` or `+` are not translated to GitLab rules.
- Not modeled at all: artifacts and caches between jobs, job outputs,
  secrets, permissions mapping and cancellation rules.
- Other forges and CI systems, such as Bitbucket, Azure Pipelines,
  Woodpecker or Jenkins, are not supported.
