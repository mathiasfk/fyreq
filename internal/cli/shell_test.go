package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
	"fyreq/internal/query"
	"fyreq/internal/storage"
)

func TestShellStatementsAndCommands(t *testing.T) {
	var out, diagnostics bytes.Buffer
	build := func(dir string) app.App { return app.App{Repository: storage.Store{Dir: dir}} }
	input := ".help\nSELECT name\nFROM files WHERE name = 'O''Brien;\\x'; SELECT count(*) FROM files;\ninvalid;\n.status\n.exit\n"
	err := RunWithInput(context.Background(), []string{"shell"}, t.TempDir(), build, strings.NewReader(input), nil, &out, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FyreQ shell.", "End SQL with ;", "Ctrl-D", ".status", "name", "count(*)", "Generation: 0", "No coverage recorded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q: %s", want, out.String())
		}
	}
	if strings.Count(diagnostics.String(), "Catalog generation 0") != 2 || !strings.Contains(diagnostics.String(), "Error:") {
		t.Fatalf("diagnostics: %s", diagnostics.String())
	}
}

func TestShellEOFAndReadFailure(t *testing.T) {
	build := func(dir string) app.App { return app.App{Repository: storage.Store{Dir: dir}} }
	for _, tc := range []struct {
		input io.Reader
		want  string
	}{
		{strings.NewReader("SELECT name FROM files"), "incomplete SQL"},
		{strings.NewReader("SELECT name FROM files\n.exit\n"), "incomplete SQL"},
		{errorReader{}, "read failed"},
	} {
		err := RunWithInput(context.Background(), []string{"shell"}, t.TempDir(), build, tc.input, nil, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("got %v, want %s", err, tc.want)
		}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestShellOutputFailure(t *testing.T) {
	build := func(dir string) app.App { return app.App{Repository: storage.Store{Dir: dir}} }
	err := RunWithInput(context.Background(), []string{"shell"}, t.TempDir(), build, strings.NewReader(".help\n"), nil, errorWriter{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("got %v", err)
	}
}

type blockingRepository struct{ started chan struct{} }

func (b blockingRepository) Load(ctx context.Context) (catalog.Snapshot, error) {
	close(b.started)
	<-ctx.Done()
	return catalog.Snapshot{}, ctx.Err()
}
func (blockingRepository) Clear(context.Context) (app.UpdateResult, error) { panic("unexpected clear") }

func (blockingRepository) Update(context.Context, func(*catalog.Snapshot) error) (app.UpdateResult, error) {
	panic("unexpected update")
}
func TestShellOperationInterrupt(t *testing.T) {
	started := make(chan struct{})
	signals := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		_, err := shellOperation(context.Background(), signals, func(ctx context.Context) (func() error, error) {
			_, err := (blockingRepository{started}).Load(ctx)
			return nil, err
		})
		done <- err
	}()
	<-started
	signals <- os.Interrupt
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

type promptWriter struct{ writes chan string }

func (w promptWriter) Write(p []byte) (int, error) { w.writes <- string(p); return len(p), nil }

func TestShellInterruptClearsPendingInput(t *testing.T) {
	input, feed := io.Pipe()
	defer input.Close()
	defer feed.Close()
	signals := make(chan os.Signal, 1)
	output := promptWriter{writes: make(chan string, 20)}
	done := make(chan error, 1)
	go func() {
		done <- runShell(context.Background(), app.App{Repository: storage.Store{Dir: t.TempDir()}}, input, signals, output, io.Discard)
	}()
	if got := <-output.writes; got != shellWelcome {
		t.Fatal(got)
	}
	if got := <-output.writes; got != "fyreq> " {
		t.Fatal(got)
	}
	if _, err := io.WriteString(feed, "SELECT name\n"); err != nil {
		t.Fatal(err)
	}
	if got := <-output.writes; got != "   ...> " {
		t.Fatal(got)
	}
	signals <- os.Interrupt
	if got := <-output.writes; got != "fyreq> " {
		t.Fatal(got)
	}
	if _, err := io.WriteString(feed, ".exit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestShellClosedInterruptChannel(t *testing.T) {
	signals := make(chan os.Signal)
	close(signals)
	err := runShell(context.Background(), app.App{Repository: storage.Store{Dir: t.TempDir()}}, strings.NewReader("SELECT name FROM files;\n.exit\n"), signals, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
}

type failAfterWrites struct{ remaining int }

func (w *failAfterWrites) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, errors.New("write failed")
	}
	w.remaining--
	return len(p), nil
}

func TestShellRenderingFailures(t *testing.T) {
	build := func(dir string) app.App { return app.App{Repository: storage.Store{Dir: dir}} }
	for _, tc := range []struct {
		name             string
		input            string
		out, diagnostics io.Writer
	}{
		{"table", "SELECT name FROM files;\n", &failAfterWrites{remaining: 2}, io.Discard},
		{"query diagnostic", "SELECT name FROM files;\n", io.Discard, errorWriter{}},
		{"error diagnostic", "invalid;\n", io.Discard, errorWriter{}},
		{"status", ".status\n", &failAfterWrites{remaining: 2}, io.Discard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RunWithInput(context.Background(), []string{"shell"}, t.TempDir(), build, strings.NewReader(tc.input), nil, tc.out, tc.diagnostics)
			if err == nil || !strings.Contains(err.Error(), "write failed") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRenderQueryPropagatesHeaderAndRowWriteFailures(t *testing.T) {
	result := app.QueryResult{Result: query.Result{Columns: []string{"name"}, Rows: [][]any{{"value"}}}}
	for _, writes := range []int{0, 1} {
		err := renderQuery(&failAfterWrites{remaining: writes}, io.Discard, result)
		if err == nil || !strings.Contains(err.Error(), "write failed") {
			t.Errorf("after %d writes: got %v", writes, err)
		}
	}
}

func TestShellRejectsFlagsAndArguments(t *testing.T) {
	build := func(dir string) app.App { return app.App{Repository: storage.Store{Dir: dir}} }
	for _, args := range [][]string{{"shell", "extra"}, {"--json", "shell"}} {
		if err := RunWithInput(context.Background(), args, t.TempDir(), build, strings.NewReader(""), nil, io.Discard, io.Discard); err == nil {
			t.Errorf("expected error for %v", args)
		}
	}
}

type cancelOnceRepository struct {
	started chan struct{}
	calls   int
}

func (r *cancelOnceRepository) Load(ctx context.Context) (catalog.Snapshot, error) {
	r.calls++
	if r.calls == 1 {
		close(r.started)
		<-ctx.Done()
		return catalog.Snapshot{}, ctx.Err()
	}
	return catalog.Snapshot{Generation: 7, Files: map[string]catalog.File{}}, nil
}
func (*cancelOnceRepository) Clear(context.Context) (app.UpdateResult, error) {
	panic("unexpected clear")
}

func (*cancelOnceRepository) Update(context.Context, func(*catalog.Snapshot) error) (app.UpdateResult, error) {
	panic("unexpected update")
}

func TestShellContinuesAfterCanceledQuery(t *testing.T) {
	input, feed := io.Pipe()
	defer input.Close()
	defer feed.Close()
	signals := make(chan os.Signal, 1)
	output := promptWriter{writes: make(chan string, 50)}
	repo := &cancelOnceRepository{started: make(chan struct{})}
	var diagnostics bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runShell(context.Background(), app.App{Repository: repo}, input, signals, output, &diagnostics)
	}()
	if got := <-output.writes; got != shellWelcome {
		t.Fatal(got)
	}
	if got := <-output.writes; got != "fyreq> " {
		t.Fatal(got)
	}
	if _, err := io.WriteString(feed, "SELECT name FROM files;\n"); err != nil {
		t.Fatal(err)
	}
	<-repo.started
	signals <- os.Interrupt
	if got := <-output.writes; got != "fyreq> " {
		t.Fatal(got)
	}
	if _, err := io.WriteString(feed, "SELECT name FROM files;\n.status\n.exit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(output.writes)
	var text strings.Builder
	for chunk := range output.writes {
		text.WriteString(chunk)
	}
	if !strings.Contains(text.String(), "name") || !strings.Contains(text.String(), "Generation: 7") {
		t.Fatalf("output: %s", text.String())
	}
	if !strings.Contains(diagnostics.String(), "context canceled") || !strings.Contains(diagnostics.String(), "Catalog generation 7") {
		t.Fatalf("diagnostics: %s", diagnostics.String())
	}
}
