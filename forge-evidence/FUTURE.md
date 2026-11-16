# Future suggestions

Ideas for forge-evidence. None of them is implemented.

## Storage

- **S3 strategy.** *Not implemented.* Store entries in an S3 bucket, for
  large or short-lived evidence that should not grow Git history.
- **Retention and cleanup.** *Not implemented.* Remove evidence for old or
  merged commits. Repository storage can only drop files from the branch;
  they stay in history.

## Access

- **REST and gRPC service.** *Not implemented.* Expose the library from
  `service/`, like the other tools.
- **Publish through forge-code.** *Not implemented.* Commit evidence with
  forge-code commits, without a local checkout.
- **From forge-actions artifacts.** *Not implemented.* Turn CI artifacts,
  such as test reports, into entries.

## Presentation

- **Change request comments.** *Not implemented.* Post evidence, or a
  summary with links, as a comment on a change request.
- **Rendering.** *Not implemented.* Show entries as readable pages or
  previews, such as images inline and test results as tables.

## Alternatives not chosen

- **Dedicated ref or orphan branch.** *Not implemented.* Evidence would live
  apart from the code it backs, so branches, merges and reviews would not
  carry it, and every tool would need extra ref handling.
- **Git LFS.** *Not implemented.* forge-checkouts does not download LFS
  content, and many forges limit or bill LFS storage. Plain files work
  everywhere.
