# 0007: Separate index and refresh commands

Status: Accepted

## Context

`index` and `refresh` were aliases accepting an optional path. Users had to
remember and repeat the roots they collected to keep the catalog current.

## Decision

- Coverage roots are the catalog's known roots.
- `index [PATH]` scans the explicit path or the platform's default roots and
  records coverage, adding the root when it is not known. Indexing a known root
  rescans it.
- `refresh` takes no arguments and rescans every known root in lexical order,
  publishing each separately. It fails when no root is known.
- Refresh lists roots from one snapshot, then confirms each root is still known
  under the writer lock before scanning it. A root removed in between, such as by
  a concurrent `clear`, is reported as skipped rather than re-added.

## Consequences

Keeping the catalog current needs no arguments. Overlapping known roots are each
scanned, so a child root is collected again within its parent's scan; this
keeps each root's coverage timestamps accurate at the cost of repeated work.
There is no command to forget one root other than `clear`.
