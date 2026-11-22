# FyreQ

FyreQ is the foundation of a filesystem metadata catalog and query engine written
in Go. It maintains one persistent catalog, collects metadata without reading file
contents, and exposes a small SQL-like language through a CLI.

The supported platforms are macOS, Linux, and Windows. Background collection,
filesystem notifications, process inspection, and a TUI are planned. The current
implementation uses only the Go standard library, including its own binary
storage format and query execution code.

## Requirements

- Go 1.24 or newer.
- macOS, Linux, or Windows 10/11. On Windows, keep the catalog on a local NTFS volume.
- Read access to the directories you want to collect. macOS privacy controls may
  require granting your terminal Full Disk Access for protected locations.

## Build and run

From the repository root:

```sh
mkdir -p bin
go build -o bin/fyreq ./cmd/fyreq
./bin/fyreq help
```

On Windows, from PowerShell:

```powershell
go build -o .\bin\fyreq.exe ./cmd/fyreq
.\bin\fyreq.exe index "$env:USERPROFILE\Documents"
.\bin\fyreq.exe status
.\bin\fyreq.exe query "SELECT path, owner_id, owner FROM files LIMIT 20"
```

You can also run the CLI without building a separate executable:

```sh
go run ./cmd/fyreq help
```

The catalog defaults to `fyreq` inside Go's user configuration directory. On
macOS this is normally `~/Library/Application Support/fyreq`; on Linux it is
`$XDG_CONFIG_HOME/fyreq`, normally `~/.config/fyreq`. On Windows it is normally `%APPDATA%\fyreq`. Use `--data-dir` to
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
FyreQ shell. End SQL with ; to execute. Use .help for help.
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

On Windows, an omitted path selects fixed drive-letter roots in letter order.
Use an explicit path such as `D:\Projects` to limit the scan. Collection can
take a long time and encounter inaccessible regions. On macOS and Linux it
crosses mounted directories encountered during traversal, including Linux
pseudo-filesystems such as `/proc` and `/sys`. Symlinks and Windows junctions
are recorded without traversing their targets. Collection starts only when
requested.

Paths select a region to update within the same catalog; they do not create
separate indexes. Each indexed path becomes a known root, recorded in coverage.
Indexing a known root rescans it. Any path can be queried, but paths not yet
collected have no records. Inspect coverage before interpreting an empty result
as absence.

Update the catalog after filesystem changes by revisiting every known root:

```sh
./bin/fyreq refresh
./bin/fyreq --json query "SELECT name, path, modified_at FROM files LIMIT 10"
./bin/fyreq --json status
```

`refresh` takes no arguments; use `index PATH` to add or rescan one root. It
rescans known roots in lexical order, each published separately, and fails when
no root is known. Overlapping roots are each scanned. A root removed after
`refresh` lists the roots, for example by a concurrent `clear`, is skipped rather
than re-added. Both commands use the same reconciliation. A complete scan
removes unobserved records within its root. An incomplete scan saves observed
records, retains all unobserved records, records diagnostics, and exits with code
1. This conservative policy can retain stale records until a complete scan of
the relevant region succeeds. Up to 100 error messages are retained per root;
the error count includes all collection errors.

Updates only write records whose metadata changed: a refresh in which one file
changed writes one small segment and a new manifest. Unchanged records keep
their `indexed_at` value, the time their current metadata was first recorded;
coverage `completed_at` reports when a root was last verified.

Collection includes the filesystem owner ID (`owner_id`) and its best-effort
account name (`owner`). The ID is a decimal UID string on macOS and Linux and a
SID string on Windows. Ownership is distinct from document authorship.
Catalogs from earlier versions, which used a JSON file, are rejected by queries and indexing; collect
into a new catalog directory with `--data-dir` to leave the old catalog untouched,
or use `clear` to discard the old catalog before collecting again. Unresolved account names are null even if the owner ID is
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

Prefix a query with `EXPLAIN` to show its plan, including how many storage
segments and blocks per-block statistics allow it to skip, without reading
records. `EXPLAIN ANALYZE` runs the query and reports the rows and bytes actually
read, the rows returned, and the execution time:

```sh
./bin/fyreq query "EXPLAIN SELECT path FROM files WHERE size > 1048576"
./bin/fyreq query "EXPLAIN ANALYZE SELECT path FROM files WHERE path LIKE '/usr/lib/%' LIMIT 10"
```

Export the whole catalog as JSON for testing and debugging:

```sh
./bin/fyreq export > catalog-export.json
```

The export is one JSON object read from a single generation: `format_version`,
`platform`, `generation`, `updated_at`, `entries`, `coverage`, the storage
`segments`, and `files`, an array of every record in ascending path order,
including the internal native `identity`. Output is streamed; if export fails
partway the output is truncated and the command exits nonzero.
## Commands

| Command | Behavior |
| --- | --- |
| `index [PATH]` | Collect a region into the global catalog and add it to the known roots; defaults to `/` on macOS and Linux or to fixed drive-letter roots on Windows |
| `refresh` | Rescan every known root |
| `query SQL` | Query recorded metadata without accessing the live filesystem; supports `EXPLAIN [ANALYZE]` |
| `status` | Show generation, entry count, and coverage |
| `export` | Write the catalog to standard output as JSON |
| `clear` | Remove all records and coverage from the selected catalog |
| `shell` | Start an interactive query session |
| `help` | Show usage |

Add `--json` before a one-shot command for structured output. Query JSON contains
`columns`, `rows`, `generation`, and `coverage`; plans add `"explain": true` and
have a single `plan` column with one row per line. `export` always writes JSON. Text query output quotes strings
to safely represent filenames containing tabs, newlines, and terminal controls.
Index and refresh JSON output is an array of per-root outcomes with coverage,
`attempted`, `skipped` for refreshed roots no longer indexed, `publication.published`,
and the published generation. A published
update with a persistence confirmation error exits nonzero while retaining the new
generation. Reopening proves visibility, not durability. Errors go to stderr;
successful commands exit with code 0, errors with code 1.

To clear the index, run:

```sh
./bin/fyreq clear
# Select a different catalog explicitly:
./bin/fyreq --data-dir /path/to/catalog clear
```

`clear` immediately removes all records and coverage from the selected catalog,
without a confirmation prompt. It does not delete the indexed filesystem entries.
It publishes an empty catalog under the same writer lock and atomic replacement
rules as indexing, starting a new generation history at 1 rather than deleting
the catalog directory. An empty or missing catalog can also be cleared. Cancellation or failure
before publication preserves the previous state; a persistence confirmation error
after publication reports the visible generation and exits nonzero. `--json clear`
returns `published` and, when published, `generation`. Unlike queries and indexing,
`clear` does not read or validate the old catalog: it can replace legacy, corrupt,
or incompatible catalogs with an empty catalog in the current format. After
publication it removes segment files the empty catalog no longer references and
any legacy `catalog.json`.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests use temporary directories and exercise query parsing, pruning, and execution,
reconciliation, symbolic links, persistence, deltas and compaction, corruption
detection, writer locking, readers concurrent with writers, cancellation, and the
CLI flow from collection through querying, export, and deletion.

## Current limits

- The catalog is stored as versioned, columnar binary segments sorted by path.
  Queries read only the columns they need and skip blocks using per-block
  minimum/maximum statistics, but there are no secondary indexes: filters on
  fields that do not cluster by path still read most blocks.
- Collection still walks the whole root, and reconciliation holds the root's
  existing records and the changed records in memory. Sorting and grouping are
  in memory; `LIMIT` with `ORDER BY` keeps only the requested rows.
- There is no daemon, periodic refresh, event watcher, live query fallback,
  content indexing, process inspection, joins, or TUI.
- Internal record IDs are retained at the same path when native identity agrees.
  Renames are not correlated, and hard links have separate path records. Native
  identity represents a filesystem observation and can be reused by the OS.
- Path and coverage keys are absolute, cleaned lexical paths with exact string
  equality. Filesystem aliases, case variants, and native
  filename encodings outside valid UTF-8 are not normalized or fully modeled.
- One writer is allowed. A crash can leave `.writer-lock` in the catalog
  directory. Verify that no writer is running before manually removing that
  empty directory. Segment files and temporary `.manifest-*.tmp` files left by an
  interrupted writer are removed by the next successful write.
- Publication targets local filesystems. Windows flushes file contents before
  replacing the manifest and does not separately confirm directory-entry
  durability. Checksums detect corruption, but there is no WAL, automatic
  corruption repair, or migration from older catalog formats. Unsupported
  versions fail explicitly; storage failures should be investigated before
  retrying a write.

See [architecture](docs/architecture.md), the [query language](docs/query-language.md),
and [architecture decisions](docs/decisions/0001-foundation.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
