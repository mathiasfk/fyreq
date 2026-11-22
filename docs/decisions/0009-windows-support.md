# 0009: Windows support

Status: Accepted. Extends 0003 and 0008.

## Context

Windows uses drive-letter and UNC paths, SID-based ownership, and different
file sharing and replacement rules. Catalog readers must keep a consistent
snapshot across concurrent writes and cleanup.

## Decision

- Use a Windows adapter to observe the volume serial number and file index and
  retrieve the owner SID from a metadata handle opened without following the
  final reparse point. Reuse the per-scan account lookup cache.
- Enumerate fixed drive-letter roots for pathless indexing, in drive-letter
  order. Explicit paths retain the lexical path rules, including case and volume
  boundaries. Directory junction targets are outside the traversal.
- On local NTFS, open manifest and segment readers with delete sharing. Publish
  with a same-volume FileRenameInfoEx replacement and retire stale files with
  FileDispositionInfoEx, both using POSIX semantics so existing readers retain
  their handles. Use the standard library and native Windows APIs.
- Flush segment and temporary manifest contents before publication. Windows
  has no equivalent of the directory sync used by the Unix adapters in this
  implementation.

## Consequences

Windows catalogs record platform `windows` and cannot be opened by the Unix
adapters. The storage target is Windows 10/11 on local NTFS. Other Windows
filesystems and network catalog locations have not been validated. Power loss
durability of directory entries is unconfirmed. Explicit scans and per-root
coverage retain their existing behavior, including conservative reconciliation
after incomplete scans.
