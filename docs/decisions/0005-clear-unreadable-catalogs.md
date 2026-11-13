# 5. Clear without reading the old catalog

## Decision

Expose clearing as a separate repository operation. Unlike ordinary updates, it
prepares a new empty snapshot without loading or validating the previous file.
This allows `fyreq clear` to discard legacy, incompatible, or corrupt catalogs.

Clearing shares writer locking, cancellation checks, atomic replacement, and
publication outcome reporting with ordinary updates. A failed or canceled clear
before publication leaves the previous file intact. Indexed filesystem entries
and unrelated files in the catalog directory are not removed.

## Consequences

The new catalog uses the current schema and platform, with empty records and
coverage. Its generation history restarts at 1 because the old generation cannot
be trusted or recovered in every case. Generations identify observations within a
catalog history, not globally unique revisions across explicit clears.

Queries and ordinary updates continue to reject unreadable catalogs. Clearing is
an explicit discard operation, not a migration or corruption repair mechanism.
