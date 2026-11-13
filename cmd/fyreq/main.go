// Command fyreq indexes filesystem metadata and queries the persistent catalog.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"fyreq/internal/app"
	"fyreq/internal/cli"
	"fyreq/internal/indexing"
	"fyreq/internal/platform"
	"fyreq/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dir, err := os.UserConfigDir()
	if err == nil {
		err = cli.Run(ctx, os.Args[1:], filepath.Join(dir, "fyreq"), func(dir string) app.App {
			return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}, Roots: platform.Roots{}}
		}, os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fyreq:", err)
		os.Exit(1)
	}
}
