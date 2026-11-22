# 0006: Versioned binary segment storage

Status: Accepted. Supersedes the JSON snapshot decision in 0001.

## Context

The catalog was a single JSON snapshot. Every update rewrote it, and every query
decoded all of it, even when a filter or projection needed a fraction of the data.
Storage must remain within the project, use only the standard library, allow the
schema to evolve, and keep atomic publication, snapshot reads concurrent with a
single writer, and explicit failures.

## Decision

- Store the catalog as a small `manifest` plus immutable segment files. The
  manifest is the only commit point and is replaced atomically by rename.
- Store path-sorted rows in segments split into blocks. Store each column of a
  block as a separate checksummed chunk, and index chunks in a footer with null
  counts and minimum and maximum values.
- Encode both file types as a magic number, a major version, and tagged fields.
  Readers skip unknown fields and columns, and read columns absent from older
  segments as null or zero. Column IDs are permanent. Incompatible changes
  increase the major version. JSON catalogs are rejected, not migrated.
- Write changed records as a delta segment overlaying disjoint base segments.
  Use tombstones for deletions. When deltas exceed a count or size bound, compact
  them into the base, rewriting only base segments whose range receives changes.
- Do not rewrite unchanged records on reconciliation. `indexed_at` therefore
  means when the current metadata was first recorded; coverage records when a
  root was last verified.
- Let queries declare the fields they need and a filter that can judge
  statistics. Storage skips segments and blocks, decodes filter columns before
  others, and returns records in path order so default-ordered queries with a
  limit stop early. Expose the same work through EXPLAIN and EXPLAIN ANALYZE.
- Readers hold open every segment of the manifest they read. Writers remove
  unreferenced segments after publishing; a reader finding a segment missing or
  replaced (identified by a random ID) retries with the newer manifest.

## Consequences

A refresh in which one file changed writes one small segment and a manifest
rather than the whole catalog. Queries filtering on path, or on values that
cluster within blocks, read a small part of the catalog, and every query reads
only its columns. Segment files remain valid while referenced, so readers never
wait for writers.

Each read loads the path column of every delta to resolve shadowing, and
compaction holds the deltas and changes in memory; the delta bounds limit both
costs. Fields that do not correlate with path order rarely prune blocks until
secondary indexes exist. Reconciliation still loads the root's existing records.
Correct cleanup depends on POSIX unlink semantics for open files, which hold on
the supported platforms. The `indexed_at` meaning changed from last observation
to last recorded change.
