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
./bin/fyreq query "SELECT path FROM files WHERE size >= 1048576 AND kind = 'file'"
./bin/fyreq query "SELECT path, uid, owner FROM files WHERE owner = 'root' LIMIT 20"
```

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

On macOS, collection includes the numeric filesystem owner (`uid`) and its
best-effort account name (`owner`). Ownership is distinct from document authorship.
Existing catalogs remain readable, but previously collected records return null
ownership until refreshed. Unresolved account names are null even if the UID is
known; numeric UID zero is a valid value. See the query language for null behavior.

Ctrl-C requests cancellation. Cancellation detected before publication discards
the pending changes; an already published generation remains committed. Queries continue to
read the previous published snapshot during an update. Coverage records the last
attempt for each requested root, including timestamps and completeness. A child
refresh does not refresh its parent's timestamp. A completed traversal is an
observation over time, not an atomic filesystem snapshot.

## Commands

| Command | Behavior |
| --- | --- |
| `index [PATH]` | Collect a region into the global catalog; default `/` |
| `refresh [PATH]` | Reconcile a region; default `/` |
| `query SQL` | Query recorded metadata without accessing the live filesystem |
| `status` | Show generation, entry count, and coverage |
| `help` | Show usage |

Add `--json` before the command for structured output. Query JSON contains
`columns`, `rows`, `generation`, and `coverage`. Text query output quotes strings
to safely represent filenames containing tabs, newlines, and terminal controls.
Errors go to stderr; successful commands exit with code 0, errors with code 1.

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
  full scan with path sorting; there are no secondary search indexes yet.
- Every update rewrites the snapshot. This foundation is intended to validate
  boundaries and behavior before introducing scalable storage structures.
- There is no daemon, periodic refresh, event watcher, live query fallback,
  content indexing, process inspection, joins, or TUI.
- Internal record IDs are retained at the same path when native identity agrees.
  Renames are not correlated, and hard links have separate path records. macOS
  device/inode identity is an observation, not an eternal identifier.
- Collection uses lexical paths. Filesystem aliases, case variants, and native
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
