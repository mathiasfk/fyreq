// Package cli translates command-line requests and renders application results.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"fyreq/internal/app"
)

const usage = `Usage: fyreq [--data-dir PATH] [--json] COMMAND

Commands:
  index [PATH]   Scan PATH into the global catalog (default: platform roots).
  refresh [PATH] Reconcile PATH using the same collection mechanism.
  query SQL      Query the persisted catalog without scanning the filesystem.
  status         Show catalog size and coverage.
  shell          Start an interactive query session.
  help           Show this help.

Examples:
  fyreq index "$HOME/Projects"
  fyreq query "SELECT path, size FROM files WHERE kind = 'file' LIMIT 20"
  fyreq --json status

Global flags must appear before the command; shell does not support --json.
Ctrl-C requests cancellation for one-shot commands. In shell it clears pending
input or requests cancellation of the current operation, and the session continues.
Changes already published remain committed. Incomplete scans are saved, reported,
and return a nonzero exit.
`

// Run leaves process lifetime, dependency construction, and exit codes to main.
func Run(ctx context.Context, args []string, defaultDir string, build func(string) app.App, out, diagnostics io.Writer) error {
	return RunWithInput(ctx, args, defaultDir, build, strings.NewReader(""), nil, out, diagnostics)
}

// RunWithInput accepts an input stream and interrupt notifications for shell sessions.
func RunWithInput(ctx context.Context, args []string, defaultDir string, build func(string) app.App, in io.Reader, interrupts <-chan os.Signal, out, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("fyreq", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	dataDir := flags.String("data-dir", defaultDir, "Catalog directory")
	asJSON := flags.Bool("json", false, "Write machine-readable JSON")
	flags.Usage = func() { fmt.Fprint(diagnostics, usage) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	remaining := flags.Args()
	if len(remaining) == 0 || remaining[0] == "help" {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	application := build(*dataDir)
	if remaining[0] == "shell" {
		if len(remaining) != 1 {
			return fmt.Errorf("shell accepts no arguments")
		}
		if *asJSON {
			return fmt.Errorf("shell does not support --json")
		}
		return runShell(ctx, application, in, interrupts, out, diagnostics)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if interrupts != nil {
		go func() {
			select {
			case <-interrupts:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	switch remaining[0] {
	case "index", "refresh":
		if len(remaining) > 2 {
			return fmt.Errorf("%s accepts at most one path", remaining[0])
		}
		root := ""
		if len(remaining) == 2 {
			root = remaining[1]
			if root == "" {
				return fmt.Errorf("path must not be empty")
			}
		}
		outcomes, updateErr := application.RefreshRoots(ctx, root)
		if *asJSON {
			if err := json.NewEncoder(out).Encode(outcomes); err != nil {
				return err
			}
		} else {
			for _, outcome := range outcomes {
				if !outcome.Attempted {
					fmt.Fprintf(out, "%q: not attempted\n", outcome.Root)
					continue
				}
				fmt.Fprintf(out, "%q: published=%t generation=%d complete=%t collection errors=%d\n", outcome.Root, outcome.Publication.Published, outcome.Publication.Generation, outcome.Coverage.Complete, outcome.Coverage.ErrorCount)
				for _, message := range outcome.Coverage.Errors {
					fmt.Fprintln(diagnostics, message)
				}
			}
		}
		if updateErr != nil {
			for _, outcome := range outcomes {
				if outcome.Publication.Published && outcome.Error != "" {
					return fmt.Errorf("generation %d was published but persistence confirmation failed: %w", outcome.Publication.Generation, updateErr)
				}
			}
		}
		return updateErr
	case "query":
		if len(remaining) != 2 {
			return fmt.Errorf("query requires one quoted SQL expression")
		}
		result, err := application.Query(ctx, remaining[1])
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(result)
		}
		return renderQuery(out, diagnostics, result)

	case "status":
		if len(remaining) != 1 {
			return fmt.Errorf("status accepts no arguments")
		}
		status, err := application.Status(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(status)
		}
		return renderStatus(out, status)

	default:
		return fmt.Errorf("unknown command %q; run fyreq help", remaining[0])
	}
}
