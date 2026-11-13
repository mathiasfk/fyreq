package cli

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"fyreq/internal/app"
)

func renderQuery(out, diagnostics io.Writer, result app.QueryResult) error {
	if _, err := fmt.Fprintf(diagnostics, "Catalog generation %d; results reflect recorded observations. Inspect coverage with status or .status.\n", result.Generation); err != nil {
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, strings.Join(result.Columns, "\t")); err != nil {
		return err
	}
	for _, row := range result.Rows {
		cells := make([]string, len(row))
		for i, value := range row {
			switch v := value.(type) {
			case nil:
				cells[i] = "NULL"
			case string:
				// Filenames can contain tabs, newlines, or terminal controls.
				cells[i] = strconv.Quote(v)
			case time.Time:
				cells[i] = v.Format(time.RFC3339Nano)
			default:
				cells[i] = fmt.Sprint(v)
			}
		}
		if _, err := fmt.Fprintln(writer, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func renderStatus(out io.Writer, status app.Status) error {
	if _, err := fmt.Fprintf(out, "Generation: %d\nEntries: %d\n", status.Generation, status.Files); err != nil {
		return err
	}
	roots := make([]string, 0, len(status.Coverage))
	for root := range status.Coverage {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	if len(roots) == 0 {
		_, err := fmt.Fprintln(out, "No coverage recorded. Run index to collect filesystem metadata.")
		return err
	}
	for _, root := range roots {
		report := status.Coverage[root]
		if _, err := fmt.Fprintf(out, "%q: complete=%t errors=%d observed=%s\n", root, report.Complete, report.ErrorCount, report.CompletedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}
