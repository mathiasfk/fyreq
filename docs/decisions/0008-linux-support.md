# 0008: Linux support

Status: Accepted. Extends 0003.

## Context

0003 kept Darwin native metadata and publication separate from fallback code
until another platform's guarantees were implemented and tested.

## Decision

- Share the native identity (device and inode), ownership (UID), default root
  (`/`), and publication code between macOS and Linux using explicit
  `darwin || linux` build constraints in `_unix.go` files. Other platforms keep
  the fallback with `!darwin && !linux`.
- Publication on Linux uses the same rename and directory sync as macOS, which
  is atomic for readers on local POSIX filesystems.
- Run the persistence, CLI, ownership, and concurrency tests on both platforms.

## Consequences

Linux catalogs record platform `linux` and are not interchangeable with macOS
catalogs. Collecting `/` on Linux also traverses pseudo-filesystems such as
`/proc` and `/sys`, which commonly produces incomplete coverage; filesystem
type exclusion and volume discovery remain future work. Network filesystems
with weaker rename or unlink semantics are not validated.
