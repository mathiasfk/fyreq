// Package cli translates command-line requests and renders application results.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"fyreq/internal/app"
)

const usage = `Usage: fyreq [--data-dir PATH] [--json] COMMAND

Commands:
  index [PATH]   Scan PATH into the global catalog (default: /).
  refresh [PATH] Reconcile PATH using the same collection mechanism.
  query SQL      Query the persisted catalog without scanning the filesystem.
  status         Show catalog size and coverage.
  help           Show this help.

Examples:
  fyreq index "$HOME/Projects"
  fyreq query "SELECT path, size FROM files WHERE kind = 'file' LIMIT 20"
  fyreq --json status

Global flags must appear before the command. Ctrl-C requests cancellation;
changes already published remain committed. Incomplete scans are saved,
reported, and return a nonzero exit.
`

// Run leaves process lifetime, dependency construction, and exit codes to main.
func Run(ctx context.Context, args []string, defaultDir string, build func(string) app.App, out, diagnostics io.Writer) error {
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
	switch remaining[0] {
	case "index", "refresh":
		if len(remaining) > 2 {
			return fmt.Errorf("%s accepts at most one path", remaining[0])
		}
		root := string(filepath.Separator)
		if len(remaining) == 2 {
			root = remaining[1]
		}
		report, err := application.Refresh(ctx, root)
		if err != nil {
			return err
		}
		if *asJSON {
			err = json.NewEncoder(out).Encode(report)
		} else {
			_, err = fmt.Fprintf(out, "Updated %s (complete: %t, collection errors: %d)\n", report.Root, report.Complete, report.ErrorCount)
		}
		if err != nil {
			return err
		}
		if !report.Complete {
			for _, message := range report.Errors {
				fmt.Fprintln(diagnostics, message)
			}
			return fmt.Errorf("scan incomplete; observed records were saved and unobserved records retained")
		}
		return nil
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
		fmt.Fprintf(diagnostics, "Catalog generation %d; results reflect recorded observations. Use status to inspect coverage.\n", result.Generation)
		writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, strings.Join(result.Columns, "\t"))
		for _, row := range result.Rows {
			cells := make([]string, len(row))
			for i, value := range row {
				switch v := value.(type) {
				case string:
					// Filenames can contain tabs, newlines, or terminal controls.
					cells[i] = strconv.Quote(v)
				case time.Time:
					cells[i] = v.Format(time.RFC3339Nano)
				default:
					cells[i] = fmt.Sprint(v)
				}
			}
			fmt.Fprintln(writer, strings.Join(cells, "\t"))
		}
		return writer.Flush()
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
		fmt.Fprintf(out, "Generation: %d\nEntries: %d\n", status.Generation, status.Files)
		roots := make([]string, 0, len(status.Coverage))
		for root := range status.Coverage {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		if len(roots) == 0 {
			_, err = fmt.Fprintln(out, "No coverage recorded. Run index to collect filesystem metadata.")
			return err
		}
		for _, root := range roots {
			report := status.Coverage[root]
			if _, err := fmt.Fprintf(out, "%q: complete=%t errors=%d observed=%s\n", root, report.Complete, report.ErrorCount, report.CompletedAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q; run fyreq help", remaining[0])
	}
}
