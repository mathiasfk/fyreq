# 0001: Modular monolith and explicit catalog reconciliation

Status: Accepted

## Context

fyreq needs a portable foundation for querying filesystem metadata, with macOS
as its initial platform. Future work includes background indexing, process
inspection, more operating systems, and a TUI. Storage and query mechanics must
be implemented within the project rather than delegated to a database engine.

## Decision

- Use Go, one module, and one CLI executable.
- Separate application operations, catalog semantics, queries, reconciliation,
  storage, platform adapters, and CLI presentation.
- Use small consumer-owned interfaces at repository and collector boundaries.
- Maintain one catalog; optional collection roots select work within it.
- Default explicit collection to `/`; never collect implicitly during a query.
- Begin with a versioned JSON snapshot and atomic publication on macOS.
- Execute a typed SQL-like subset using a deterministic full scan.
- Persist coverage and retain unobserved entries after incomplete collection.
- Keep repository code, documentation, and agent instructions in English.

## Consequences

The first implementation is usable and testable without committing to a disk
tree, journal, daemon protocol, or public library API. The CLI and a future TUI
can share application operations. Native collection can evolve independently of
the query language.

Full-snapshot storage and in-memory scans are not suitable for the eventual
large-scale performance goal. Updates require memory proportional to the catalog
and rewrite the entire file. Query execution also materializes its result.
Custom scalable storage and access-path selection are subsequent work.

Directory locking prevents concurrent writers but leaves recovery work after a
crash. Conservative incomplete-scan handling can retain stale records. Path-based
record reconciliation does not yet track renames or unify hard links. These
limitations are explicit and must remain documented until replaced.
