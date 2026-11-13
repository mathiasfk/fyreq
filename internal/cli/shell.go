package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"fyreq/internal/app"
)

const shellWelcome = `FyreQ shell. End SQL with ; to execute.
.help for help; .status for coverage; .exit or Ctrl-D at an empty prompt to exit.
Ctrl-C clears pending input or requests cancellation of the current operation.
`

const shellHelp = `Enter SQL ending in a semicolon. Strings use doubled single quotes.
.help    Show this help.
.status  Show catalog coverage.
.exit    Exit the shell.
Ctrl-C clears pending input or requests cancellation of the current operation.
`

type inputLine struct {
	text string
	err  error
}

// splitStatement follows the query lexer: only single quotes delimit strings,
// doubled quotes represent a quote, and backslashes have no special meaning.
func splitStatement(s string) (string, string, bool) {
	quoted := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			if quoted && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			quoted = !quoted
		} else if s[i] == ';' && !quoted {
			return s[:i+1], s[i+1:], true
		}
	}
	return "", s, false
}

func runShell(ctx context.Context, application app.App, in io.Reader, interrupts <-chan os.Signal, out, diagnostics io.Writer) error {
	if _, err := fmt.Fprint(out, shellWelcome); err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	requests := make(chan struct{})
	lines := make(chan inputLine, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-requests:
			}
			line, err := reader.ReadString('\n')
			select {
			case lines <- inputLine{line, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var pending string
	var reading bool
	var eof bool
	for {
		if statement, rest, ok := splitStatement(pending); ok {
			pending = rest
			if strings.TrimSpace(statement) == ";" {
				continue
			}
			render, err := shellOperation(ctx, interrupts, func(opctx context.Context) (func() error, error) {
				result, err := application.Query(opctx, statement)
				return func() error { return renderQuery(out, diagnostics, result) }, err
			})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if _, writeErr := fmt.Fprintf(diagnostics, "Error: %v\n", err); writeErr != nil {
					return writeErr
				}
			} else if err := render(); err != nil {
				return err
			}
			continue
		}
		if eof {
			if strings.TrimSpace(pending) != "" {
				return fmt.Errorf("incomplete SQL at end of input (missing semicolon or closing quote)")
			}
			return nil
		}
		if !reading {
			prompt := "fyreq> "
			if strings.TrimSpace(pending) != "" {
				prompt = "   ...> "
			}
			if _, err := fmt.Fprint(out, prompt); err != nil {
				return err
			}
			select {
			case requests <- struct{}{}:
				reading = true
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-interrupts:
			if !ok {
				interrupts = nil
				continue
			}
			pending = ""
			if _, err := fmt.Fprintln(diagnostics, "Input cleared."); err != nil {
				return err
			}
			if _, err := fmt.Fprint(out, "fyreq> "); err != nil {
				return err
			}
		case line := <-lines:
			reading = false
			if line.err != nil && !errors.Is(line.err, io.EOF) {
				return line.err
			}
			eof = errors.Is(line.err, io.EOF)
			if strings.TrimSpace(pending) == "" && strings.HasPrefix(strings.TrimSpace(line.text), ".") {
				switch strings.TrimSpace(line.text) {
				case ".exit":
					return nil
				case ".help":
					if _, err := fmt.Fprint(out, shellHelp); err != nil {
						return err
					}
				case ".status":
					render, err := shellOperation(ctx, interrupts, func(opctx context.Context) (func() error, error) {
						status, err := application.Status(opctx)
						return func() error { return renderStatus(out, status) }, err
					})
					if err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						if _, writeErr := fmt.Fprintf(diagnostics, "Error: %v\n", err); writeErr != nil {
							return writeErr
						}
					} else if err := render(); err != nil {
						return err
					}
				default:
					if _, err := fmt.Fprintln(diagnostics, "Unknown command; use .help."); err != nil {
						return err
					}
				}
			} else {
				pending += line.text
			}
		}
	}
}

type operationResult struct {
	render func() error
	err    error
}

func shellOperation(ctx context.Context, interrupts <-chan os.Signal, op func(context.Context) (func() error, error)) (func() error, error) {
	opctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan operationResult, 1)
	go func() { render, err := op(opctx); results <- operationResult{render, err} }()
	for {
		select {
		case result := <-results:
			return result.render, result.err
		case _, ok := <-interrupts:
			if ok {
				cancel()
			}
			interrupts = nil
		case <-ctx.Done():
			cancel()
			result := <-results
			return result.render, ctx.Err()
		}
	}
}
