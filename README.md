# FyreQ

FyreQ is the foundation of a filesystem metadata catalog and query engine written
in Go. It maintains one persistent catalog, collects metadata without reading file
contents, and exposes a small SQL-like language through a CLI.

The initial target is macOS. Linux and Windows support, background collection,
filesystem notifications, process inspection, and a TUI are planned. The current
implementation uses only the Go standard library, including its own persistence
and query execution code.

## Requirements

- Go 1.24 or newer.
- macOS for the supported runtime. Other operating systems are not validated yet.
- Read access to the directories you want to collect. macOS privacy controls may
  require granting your terminal Full Disk Access for protected locations.

## Build and run

From the repository root:

```sh
mkdir -p bin
go build -o bin/fyreq ./cmd/fyreq
./bin/fyreq help
```

You can also run the CLI without building a separate executable:

```sh
go run ./cmd/fyreq help
```

The catalog defaults to `fyreq` inside Go's user configuration directory. On
macOS this is normally `~/Library/Application Support/fyreq`. Use `--data-dir` to
choose a different location. Global flags must precede the command.

## Quick start

Collect a small directory first:

```sh
./bin/fyreq index "$HOME/Projects"
./bin/fyreq status
./bin/fyreq query "SELECT path, size FROM files WHERE kind = 'file' LIMIT 20"
./bin/fyreq query "SELECT path, size FROM files WHERE kind = 'file' ORDER BY size DESC LIMIT 20"
./bin/fyreq query "SELECT path FROM files WHERE size >= 1048576 AND kind = 'file'"
./bin/fyreq query "SELECT path, owner_id, owner FROM files WHERE owner = 'root' LIMIT 20"
./bin/fyreq query "SELECT kind, COUNT(*) FROM files GROUP BY kind"
./bin/fyreq query "SELECT owner, SUM(size) FROM files WHERE kind = 'file' GROUP BY owner ORDER BY SUM(size) DESC LIMIT 10"
```

Start a query session against the same catalog:

```text
./bin/fyreq shell
fyreq> SELECT name, size
   ...> FROM files WHERE kind = 'file' LIMIT 20;
fyreq> .status
fyreq> .exit
```

SQL executes at a semicolon outside a single-quoted string; doubled quotes inside
strings represent a quote. Multiple statements can share a line. Enter `.help`,
`.status`, or `.exit` on a line by itself when no SQL is pending. An unfinished
statement at end of input is an error; `.exit` within pending SQL is part of that
statement. Query errors are reported and the session continues. The shell does
not accept `--json`.

Ctrl-C clears accumulated SQL while waiting for a line, or requests cooperative
cancellation of the current query or status operation. Cancellation may wait for
non-interruptible work such as loading or sorting. Ctrl-D exits at an empty prompt.
Input uses standard line-oriented terminal handling: editing and the fate of
characters typed on the current line after Ctrl-C depend on the terminal. With
redirected or custom input, an interrupted read can remain blocked until input
arrives; the shell cannot discard characters held by the reader. No history or
completion is provided.

To collect the accessible filesystem, omit the path:

```sh
./bin/fyreq index
# Equivalent to: ./bin/fyreq index /
```

This can take a long time, crosses mounted directories encountered during
traversal, and will commonly encounter inaccessible regions. It never starts
automatically when opening the CLI or querying. Symlinks are recorded without
following them, so a symlink to another tree does not make that tree covered.
There is no separate volume discovery service yet.

Paths select a region to update within the same catalog; they do not create
separate indexes. Any path can be queried, but paths not yet collected have no
records. Inspect coverage before interpreting an empty result as absence.

Update the catalog after filesystem changes:

```sh
./bin/fyreq refresh "$HOME/Projects"
./bin/fyreq --json query "SELECT name, path, modified_at FROM files LIMIT 10"
./bin/fyreq --json status
```

`index` and `refresh` currently perform the same reconciliation. A complete scan
removes unobserved records within its root. An incomplete scan saves observed
records, retains all unobserved records, records diagnostics, and exits with code
1. This conservative policy can retain stale records until a complete scan of
the relevant region succeeds. Up to 100 error messages are retained per root;
the error count includes all collection errors.

On macOS, collection includes the filesystem owner ID (`owner_id`, a decimal UID string) and its
best-effort account name (`owner`). Ownership is distinct from document authorship.
Version-1 catalogs lack a platform and are rejected; collect into a new catalog
directory with `--data-dir`. The old catalog is left untouched. Unresolved account names are null even if the UID is
known; UID zero is represented as `"0"`. Use `IS NULL` and `IS NOT NULL` to filter unavailable values:

```sh
./bin/fyreq query "SELECT path, owner_id FROM files WHERE owner IS NULL"
./bin/fyreq query "SELECT path FROM files WHERE owner_id IS NOT NULL AND owner IS NULL"
```

See the query language for null behavior.

Ctrl-C requests cancellation. Cancellation detected before publication discards
the pending changes; an already published generation remains committed. Queries continue to
read the previous published snapshot during an update. Coverage records the last
attempt for each requested root, including timestamps and completeness. A child
refresh does not refresh its parent's timestamp. A completed traversal is an
observation over time, not an atomic filesystem snapshot.

Boolean filters support `NOT`, `AND`, and `OR`, in that precedence order.
Parentheses override precedence:

```sh
./bin/fyreq query "SELECT path FROM files WHERE owner = 'root' OR kind = 'directory'"
./bin/fyreq query "SELECT path FROM files WHERE NOT (kind = 'directory' OR size = 0)"
```

Use `LIKE` and `NOT LIKE` for case-sensitive string patterns (`%` for zero or
more characters, `_` for one). `ESCAPE` makes wildcard characters literal:

```sh
./bin/fyreq query "SELECT path FROM files WHERE name LIKE '%.go' OR name LIKE '%.md'"
./bin/fyreq query "SELECT path FROM files WHERE kind = 'file' AND name NOT LIKE 'test_%'"
./bin/fyreq query "SELECT path FROM files WHERE name LIKE 'report!_!%.txt' ESCAPE '!'"
```

## Commands

| Command | Behavior |
| --- | --- |
| `index [PATH]` | Collect a region into the global catalog; default `/` |
| `refresh [PATH]` | Reconcile a region; default `/` |
| `query SQL` | Query recorded metadata without accessing the live filesystem |
| `status` | Show generation, entry count, and coverage |
| `shell` | Start an interactive query session |
| `help` | Show usage |

Add `--json` before a one-shot command for structured output. Query JSON contains
`columns`, `rows`, `generation`, and `coverage`. Text query output quotes strings
to safely represent filenames containing tabs, newlines, and terminal controls.
Index and refresh JSON output is an array of per-root outcomes with coverage,
`attempted`, `publication.published`, and the published generation. A published
update with a persistence confirmation error exits nonzero while retaining the new
generation. Reopening proves visibility, not durability. Errors go to stderr;
successful commands exit with code 0, errors with code 1.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests use temporary directories and exercise query parsing and execution,
reconciliation, symbolic links, persistence, writer locking, cancellation, and the
CLI flow from collection through querying and deletion.

## Current limits

- The catalog is a versioned JSON snapshot loaded into memory. Queries perform a
  full scan with in-memory sorting; there are no secondary search indexes yet.
- Every update rewrites the snapshot. This foundation is intended to validate
  boundaries and behavior before introducing scalable storage structures.
- There is no daemon, periodic refresh, event watcher, live query fallback,
  content indexing, process inspection, joins, or TUI.
- Internal record IDs are retained at the same path when native identity agrees.
  Renames are not correlated, and hard links have separate path records. macOS
  device/inode identity is an observation, not an eternal identifier.
- Path and coverage keys are absolute, cleaned lexical paths with exact string
  equality. Filesystem aliases, case variants, and native
  filename encodings outside valid UTF-8 are not normalized or fully modeled.
- One writer is allowed. A crash can leave `.writer-lock` in the catalog
  directory. Verify that no writer is running before manually removing that
  empty directory. Old temporary `.catalog-*.tmp` files can also be removed
  when no writer is running.
- Snapshot replacement and directory syncing target macOS. There is no WAL,
  automatic corruption repair, or schema migration. Unsupported versions fail
  explicitly; storage failures should be investigated before retrying a write.

See [architecture](docs/architecture.md), the [query language](docs/query-language.md),
and [architecture decisions](docs/decisions/0001-foundation.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
