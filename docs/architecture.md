# Architecture

## Scope

FyreQ is a modular monolith with a global, persistent filesystem metadata
catalog. A requested root limits collection work, not the lifetime or identity
of the catalog. Queries read persisted observations; they do not implicitly scan
the live filesystem.

The foundation targets macOS, a CLI, explicit collection, custom persistence,
and a small query language. Future OS adapters, a background process, and a TUI
should reuse the same application operations.

## Modules and dependencies

```text
cmd/fyreq (composition and process lifetime)
  -> cli (arguments, shell session, and presentation)
       -> app (use cases)
            -> query (parse, validate, execute)
            -> indexing (reconcile observations)
            -> Repository interface

storage implements app.Repository
platform implements indexing.Collector
catalog defines the shared metadata and coverage model
```

The CLI shell reads line-oriented input, dispatches queries and status directly to
the application, and shares result rendering with one-shot commands. It has no
terminal editing or history layer; query operations use per-operation contexts.

The composition root supplies concrete adapters. The application depends on a
repository contract; indexing depends on a collector contract. Query execution
receives records, not storage handles. OS-specific identity extraction is isolated
in `identity_darwin.go`; other platforms have a portable fallback but are not
supported or validated yet.

The query plan contains validated field or aggregate selections, a conjunction
of typed predicates, optional grouping keys, ordering terms, and a limit.
Execution filters records, optionally groups and accumulates aggregates, sorts,
applies the limit, and projects columns. Group state is held in memory, with one
accumulator per distinct aggregate per group. Tuple keys encode boundaries and
nulls explicitly; timestamp keys normalize to UTC. Integer sums use exact
intermediate arithmetic and fail on signed 64-bit output overflow; averages use
floating-point output. Nulls sort last in both directions. Ascending lexical
path breaks ties for ordinary queries; ascending grouping tuples break ties for
grouped queries. Those tie-breakers also supply the default order.
There is no cost-based optimizer or physical access-path selection yet. Future
storage readers can expose iterators and secondary indexes without putting disk
formats in the parser or terminal rendering in the executor.

## Catalog model

Each path record contains an internal ID, absolute lexical path, basename, kind,
size, modification time, observation time, optional native identity, and nullable
filesystem owner ID and account name. Kinds
are `file`, `directory`, `symlink`, and `other`. Size is the OS-reported byte size;
directory size is not a recursive total, and symlink metadata describes the link.

The macOS adapter observes device and inode. Records retain their ID when
reobserved at the same path with matching identity. A known replacement receives
a new ID. Rename correlation and a normalized multi-path identity model remain
future work. macOS ownership comes from the entry's native metadata; account names
are resolved best-effort and cached per scan, including failed lookups. Version-1 snapshots lack a platform and are rejected without modification; collect
into a new catalog directory. UID zero is stored as the string `"0"`, distinct
from unavailable ownership. Birth times and
permissions are not exposed yet; optional metadata must distinguish absence from
zero.

Coverage records the last scan attempt per requested root. It includes start and
completion timestamps, completeness, error count, and bounded diagnostics. Roots
may overlap; each timestamp applies only to that scan. Coverage is observational,
not a promise that data is current now.

## Collection and publication

1. Acquire the catalog writer lock and load the last snapshot.
2. Walk the requested root, including the root itself, without following symlinks.
3. Upsert observations and track paths seen during this attempt.
4. On a complete traversal, remove unseen records under that root only.
5. On collection errors, retain unseen records and mark coverage incomplete.
6. On cancellation or fatal callback failure, discard the entire mutation.
7. Increment the generation and publish the new snapshot.

Retaining all unseen records after any collection error is deliberately
conservative. Future region-level reconciliation can safely remove entries in
fully traversed subtrees even when a different subtree fails.

Standard directory traversal reaches mounted directories present in the tree.
There is no explicit volume enumeration, exclusion policy, scheduling, or native
event source yet. A future daemon should own the same update operation, combine
events with periodic reconciliation, and remain the single catalog writer.

## Storage and consistency

`catalog.json` holds a schema-versioned snapshot with generation, update time,
path records, and coverage. The format is validated on load. Missing catalogs
load as empty generation zero; corrupt or unsupported catalogs fail explicitly.

A writer creates `.writer-lock` with exclusive directory creation before reading
the current snapshot. A second writer fails immediately. This avoids lost updates
across processes but requires manual stale-lock recovery after a crash.

Publication writes a private temporary file in the catalog directory, syncs and
closes it, replaces `catalog.json` by rename, and syncs the directory. On macOS,
readers opening the catalog see either the previous file or the replacement.
Readers do not wait for collection. A failure after rename can mean the new
generation is visible even though durability confirmation failed; the result reports publication and its generation even when confirmation fails.
Reopening confirms visibility, not durability. Unsupported platforms explicitly
reject publication.

Paths and coverage roots are absolute, cleaned lexical keys with exact component
comparison. Aliases and case variants are not unified. Each catalog records its
origin platform and is rejected on a different platform before paths are validated.
No cross-platform conversion is attempted. Default roots are resolved before
sequential per-root updates; macOS currently selects `/`. Earlier publications
remain committed if a later root fails. Missing discovered roots do not authorize
deletion.

The catalog is not a filesystem transaction: files can change during a scan.
Observations are eventually reconciled by explicit updates. There is no automatic
expiration, crash-recovery log, migration, or guarantee against hardware failure.

## Planned growth

- Replace full-snapshot persistence with custom record/page storage and secondary
  indexes after workloads and data semantics are established.
- Add bounded-memory query iterators and access-path planning.
- Add OS-native change notification, region-level coverage, volume discovery,
  exclusion policies, and a single-writer daemon.
- Add process and open-file relations with explicit live-observation semantics.
- Add Linux and Windows adapters, accounting for native identity and path rules.
- Add a TUI that consumes the application layer.

These are extension points, not implemented features or commitments to a
particular storage data structure.
