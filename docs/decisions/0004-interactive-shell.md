# 4. Line-oriented interactive shell

## Decision

Provide `fyreq shell` as a CLI session over the existing application operations.
SQL is buffered until a semicolon outside a single-quoted string, using the query
language's doubled-quote rules. Query and status rendering is shared with the
one-shot CLI. Dot commands are recognized only on a line with no pending SQL.
Incomplete input at EOF is an error. The shell does not support JSON output.

The process forwards interrupt signals to the CLI. While waiting for input an
interrupt clears the accumulated statement; during an operation it cancels a
per-operation context and the session continues after the operation returns.
Cancellation is cooperative. Input uses a standard-library buffered reader and
terminal line discipline, without raw mode, editing, history, or completion. A
reader already blocked on input cannot generally be interrupted or have its
buffered characters discarded; this also means an externally canceled session
may leave its input reader goroutine blocked until the reader returns. Normal EOF
and `.exit` finish without a pending read.

## Consequences

This provides a small reusable session without a terminal dependency or access to
storage outside the application layer. A future editor or TUI can replace input
and presentation while retaining the application operations. Rich terminal
control and portable immediate input cancellation require a later design.
