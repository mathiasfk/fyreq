# 0003: Portability boundaries

Status: Accepted

Catalog version 2 records its origin platform. Catalogs are platform-specific and
version 1 lacks sufficient information to determine its origin; users must collect
into a new directory. Owner IDs are nullable strings interpreted in that platform's
native authority, not globally comparable account identifiers.

Paths and coverage roots use absolute cleaned lexical keys and exact component
comparison. Case variants and aliases are not unified. Native path equivalence,
volume discovery, Windows reparse classification, and cross-platform conversion
are future work.

Each root is collected and published separately. An incomplete traversal publishes
observations without deleting unseen entries. Fatal collection errors and
cancellation before publication discard the working snapshot. A published result
includes the generation even when subsequent persistence confirmation fails.
Reopening confirms visibility rather than durability. macOS uses rename and
parent-directory sync; unsupported platforms reject mutation until their own
publication guarantees are implemented.

Collectors invoke callbacks synchronously and sequentially; callbacks cease before
Scan returns. Symlinks are recorded without following targets, while mounted
directories encountered during macOS traversal are visited. Native identity is
optional and compared only at the same path. It does not imply rename tracking or
hard-link unification and may be reused by the filesystem. Future Windows adapters
must coordinate volume roots and avoid following symlinks and junction targets.

Shared Unix code must use explicit constraints for the operating systems actually
assessed; a `_unix.go` filename alone does not select Unix targets. Keep the
current Darwin native metadata and publication code separate from fallback code
until another platform's guarantees have been implemented and tested.
