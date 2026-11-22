# Architecture

## Scope

FyreQ is a modular monolith with a global, persistent filesystem metadata
catalog. A requested root limits collection work, not the lifetime or identity
of the catalog. Queries read persisted observations; they do not implicitly scan
the live filesystem.

The foundation targets macOS, Linux, and Windows, a CLI, explicit collection, custom
binary persistence, and a small query language. Future OS adapters, a background process, and a TUI
should reuse the same application operations.

## Modules and dependencies

```text
cmd/fyreq (composition and process lifetime)
  -> cli (arguments, shell session, and presentation)
       -> app (use cases)
            -> query (parse, validate, execute)
            -> indexing (reconcile observations)
            -> Repository interface

storage implements app.Repository, app.Snapshot (a query.Source), and app.Transaction
platform implements indexing.Collector
catalog defines the shared metadata and coverage model
```

The CLI shell reads line-oriented input, dispatches queries and status directly to
the application, and shares result rendering with one-shot commands. It has no
terminal editing or history layer; query operations use per-operation contexts.

The composition root supplies concrete adapters. The application depends on a
repository contract; indexing depends on collector and working-catalog contracts.
Query execution receives a `query.Source`, not storage handles: it states which
fields it needs and offers a filter that can judge groups of records by their
statistics, and the source returns matching records in ascending path order.
Native metadata extraction and default roots live in OS-specific files in
`internal/platform`. Other platforms use the portable collection fallback and
reject catalog publication.

The query plan contains validated field or aggregate selections, a tree
of typed predicates, optional grouping keys, ordering terms, a limit, and an
optional EXPLAIN mode. Execution requests the needed fields from the source,
filters records, optionally groups and accumulates aggregates, sorts, applies
the limit, and projects columns. When the result order is ascending path (the
default order), rows stream in source order and the scan stops at the limit.
Other limited orderings keep a bounded top-N heap; unlimited ones sort all
matches. Group state is held in memory, with one
accumulator per distinct aggregate per group. Tuple keys encode boundaries and
nulls explicitly; timestamp keys normalize to UTC. Integer sums use exact
intermediate arithmetic and fail on signed 64-bit output overflow; averages use
floating-point output. Nulls sort last in both directions. Ascending lexical
path breaks ties for ordinary queries; ascending grouping tuples break ties for
grouped queries. Those tie-breakers also supply the default order.
Pruning evaluates the filter over per-group minimum, maximum, null, and row
counts, computing the set of truth values the group could produce; a group is
skipped only when true is impossible. The set over-approximates, so pruning
never removes a match. `LIKE` uses its literal prefix as a lexical range. There
is no cost-based optimizer or secondary index yet. EXPLAIN describes the plan
and the source's metadata-only pruning estimate; EXPLAIN ANALYZE executes the
query, discards its rows, and reports actual work.

## Catalog model

Each path record contains an internal ID, absolute lexical path, basename, kind,
size, modification time, recording time, optional native identity, and nullable
filesystem owner ID and account name. The recording time (`indexed_at`) is when
the current metadata values were first observed: reconciliation does not
rewrite records whose other values are unchanged, so writes scale with changes. Kinds
are `file`, `directory`, `symlink`, and `other`. Size is the OS-reported byte size;
directory size is not a recursive total, and symlink metadata describes the link.

The macOS and Linux adapter observes device and inode. On Windows, identity is
the volume serial number and file index, and ownership uses a SID. Records retain their ID when
reobserved at the same path with matching identity. A known replacement receives
a new ID. Rename correlation and a normalized multi-path identity model remain
future work. Ownership comes from the entry's native metadata; account names
are resolved best-effort and cached per scan, including failed lookups. UID zero is stored as the string `"0"`, distinct
from unavailable ownership. Birth times and
permissions are not exposed yet; optional metadata must distinguish absence from
zero.

Coverage records the last scan attempt per requested root. It includes start and
completion timestamps, completeness, error count, and bounded diagnostics. Roots
may overlap; each timestamp applies only to that scan. Coverage is observational,
not a promise that data is current now. Coverage roots are the catalog's known
roots: `index` adds one by scanning it, and `refresh` rescans all of them.

## Collection and publication

1. Acquire the catalog writer lock and open the current generation.
2. For refresh, confirm the root is still known; otherwise skip it.
3. Read the existing records within the root.
4. Walk the requested root, including the root itself, without following symlinks.
5. Buffer new and changed observations and track paths seen during this attempt.
6. On a complete traversal, buffer deletions of unseen records under that root only.
7. On collection errors, retain unseen records and mark coverage incomplete.
8. On cancellation or fatal callback failure, discard the buffered changes.
9. Write the changes as segments, increment the generation, and publish a manifest.

`index` resolves its roots (explicit or platform defaults) and `refresh` lists the
known roots before updating them one at a time. Step 2 runs under the writer
lock, so a root removed by a concurrent `clear` after listing is not recreated.

Retaining all unseen records after any collection error is deliberately
conservative. Future region-level reconciliation can safely remove entries in
fully traversed subtrees even when a different subtree fails.

Standard directory traversal reaches mounted directories on macOS and Linux.
Windows selects fixed drive-letter roots by default and does not traverse
junctions. There is no exclusion policy, scheduling, or native event source yet. A future daemon should own the same update operation, combine
events with periodic reconciliation, and remain the single catalog writer.

## Storage and consistency

The catalog directory holds a `manifest` and immutable segment files
(`<random id>.fqs`). The manifest is the single commit point. It records the
format version, platform, generation, update time, live entry count, coverage,
and the live segments in two levels: base segments with disjoint, ascending path
ranges, and delta segments ordered oldest first. Missing catalogs open as empty
generation zero; corrupt, unsupported, foreign-platform, or legacy JSON catalogs
fail explicitly.

A segment stores path-sorted rows in blocks of up to 1024 rows. Each column of a
block is a separate checksummed chunk: strings are prefix-compressed, integers
are varints, and timestamps are varint second deltas with nanoseconds. A footer
indexes chunks with each chunk's null count and minimum and maximum values.
Readers fetch only the chunks they need: path, the delta tombstone flag, and the
filter columns first, then the remaining requested columns only for blocks with
matches. Segment- and block-level statistics let a query skip data entirely.

Both file types are versioned and use tagged fields. Readers skip unknown fields
and segment columns, and treat columns absent from older segments as null or zero,
so fields and columns can be added without a new major version. Column IDs are
never reused. Incompatible changes increase the major version, which readers
reject explicitly.

Reads merge the levels in path order: a delta row overrides base rows and older
delta rows for the same path, and a delta tombstone hides it. Shadowing is
resolved from all delta paths before filtering, so a filtered-out newer row
still hides an older match. Every read therefore loads the delta path columns,
a cost bounded by compaction.

An update with no changed records writes only a new manifest. Otherwise its changes
become one new delta segment. When there would be more than 8 deltas, or more
delta rows than 4096 plus 1/64 of the live entries, the update instead compacts:
it folds all deltas and the changes into the base, rewriting only base segments
whose path range receives a change and splitting segments above 65,536 rows. Other
base segments are kept unchanged. Compaction holds the deltas, the changes,
and one base segment at a time in memory.

A writer creates `.writer-lock` with exclusive directory creation before reading
the current manifest. A second writer, or a clear, fails immediately. This avoids
lost updates across processes but requires manual stale-lock recovery after a
crash.

On macOS and Linux, publication writes and syncs new segments with unique names, syncs the directory,
writes and syncs a private temporary manifest, replaces `manifest` by rename,
and syncs the directory. Windows flushes individual files, replaces the manifest
with a same-volume Windows rename, and uses delete sharing with POSIX-style
rename and deletion. It does not separately flush directory entries. Readers see either the previous or the new manifest.
After publication the writer removes segment files the new manifest does not
reference, including files left by interrupted writers. A reader opens the
manifest and then every segment it names, verifying each segment's random ID; a
missing or replaced segment means a newer generation was published, so it
retries with the new manifest. Once opened, a snapshot is unaffected by later
unlinking, and readers never wait for writers. A failure after rename can mean
the new generation is visible even though durability confirmation failed; the
result reports publication and its generation even when confirmation fails.
Reopening confirms visibility, not durability. Unsupported platforms explicitly
reject publication.

`clear` is a distinct repository operation that publishes a new empty manifest
without reading or validating the old catalog. It resets generation history to 1
and uses the current format and platform, allowing recovery from legacy or corrupt
catalogs. It shares the writer lock and publication path with ordinary updates;
cancellation and failures before publication preserve the old files. After
publication it removes unreferenced segments and a legacy `catalog.json`; it does
not remove the directory. Queries and indexing still validate the existing
catalog before using it.

Paths and coverage roots are absolute, cleaned lexical keys with exact component
comparison. Aliases and case variants are not unified. Each catalog records its
origin platform and is rejected on a different platform before paths are validated.
No cross-platform conversion is attempted. Default roots are resolved before
sequential per-root updates; macOS and Linux select `/`, and Windows selects fixed drive-letter roots in
letter order. Earlier publications
remain committed if a later root fails. Missing discovered roots do not authorize
deletion.

The catalog is not a filesystem transaction: files can change during a scan.
Observations are eventually reconciled by explicit updates. There is no automatic
expiration, crash-recovery log, migration, or guarantee against hardware failure.

## Planned growth

- Add secondary indexes and access-path planning for fields that do not cluster
  by path, and bounded-memory sorting and grouping.
- Stream reconciliation against the stored path order instead of loading the
  root's records, and skip unchanged subtrees once change notification exists.
- Add OS-native change notification, region-level coverage, volume discovery,
  exclusion policies, and a single-writer daemon.
- Add process and open-file relations with explicit live-observation semantics.
- Add a TUI that consumes the application layer.

These are extension points, not implemented features or commitments to a
particular storage data structure.
