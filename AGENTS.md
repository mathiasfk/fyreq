# Working on FyreQ

## Language

All repository content must be in English: code identifiers, comments,
documentation, CLI output, commit descriptions, and agent instructions.

## Architecture

- Keep a single Go module and executable until a concrete requirement warrants
  another deployment boundary.
- Keep `cmd/fyreq` limited to dependency construction and process lifetime.
- Keep terminal formatting in `internal/cli` and orchestration in `internal/app`.
- Keep query parsing and execution independent of OS APIs and persistence formats.
- Define small interfaces at consumption boundaries. Do not add interfaces for
  every concrete type or speculative plugin systems.
- Keep OS-specific metadata access in `internal/platform`, using filename
  suffixes or build tags. macOS is the initial supported runtime. A `_unix.go`
  suffix is not a build constraint; explicitly select assessed targets.
- Implement storage and query mechanics in this repository. Do not introduce
  external databases or storage engines.
- Preserve previous catalog state when collection is canceled or a mutation
  fails. An incomplete scan must not infer deletions from missing observations.
- Keep record identity separate from paths. Do not claim rename tracking until
  it is implemented and tested.
- Expose coverage and observation times; an empty result is not proof of absence.

## Implementation and verification

Use the Go standard library unless a new dependency has a clear, documented
reason. Format Go changes with `gofmt`. Keep tests next to their packages and use
temporary directories instead of scanning the developer's filesystem. Cover
meaningful behavior and failure paths rather than duplicating implementation.

Run the following checks before delivering implementation changes:

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o bin/fyreq ./cmd/fyreq
```

Update README examples and query documentation whenever user-facing behavior
changes. Clearly distinguish implemented behavior from planned features. Record
material architecture decisions in `docs/decisions/`.
