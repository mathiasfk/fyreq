package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
)

func renderQuery(out, diagnostics io.Writer, result app.QueryResult) error {
	if _, err := fmt.Fprintf(diagnostics, "Catalog generation %d; results reflect recorded observations. Inspect coverage with status or .status.\n", result.Generation); err != nil {
		return err
	}
	if result.Explain {
		for _, row := range result.Rows {
			line, _ := row[0].(string)
			// Plan lines echo query literals, which can contain terminal controls.
			if !utf8.ValidString(line) || strings.IndexFunc(line, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
				line = strconv.Quote(line)
			}
			if _, err := fmt.Fprintln(out, line); err != nil {
				return err
			}
		}
		return nil
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

// renderExport streams one snapshot as a JSON object: the header fields, the
// storage segments, and every record in ascending path order. Output already
// written stays truncated if the export fails.
func renderExport(ctx context.Context, application app.App, out io.Writer) error {
	writer := bufio.NewWriter(out)
	first := true
	err := application.Export(ctx, func(header catalog.Header, segments []catalog.Segment) error {
		if segments == nil {
			segments = []catalog.Segment{}
		}
		encoded, err := json.Marshal(struct {
			catalog.Header
			Segments []catalog.Segment `json:"segments"`
		}{header, segments})
		if err != nil {
			return err
		}
		writer.Write(encoded[:len(encoded)-1])
		_, err = writer.WriteString(`,"files":[`)
		return err
	}, func(file catalog.File) error {
		if !first {
			writer.WriteByte(',')
		}
		first = false
		encoded, err := json.Marshal(file)
		if err != nil {
			return err
		}
		_, err = writer.Write(encoded)
		return err
	})
	if err != nil {
		writer.Flush()
		return err
	}
	if _, err := writer.WriteString("]}\n"); err != nil {
		return err
	}
	return writer.Flush()
}
