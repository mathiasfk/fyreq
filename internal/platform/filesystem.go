// Package platform provides operating-system adapters for filesystem collection.
package platform

import (
	"context"
	"io/fs"
	"os/user"
	"path/filepath"
	"time"

	"fyreq/internal/catalog"
)

// Filesystem collects metadata without opening file contents or following links.
type Filesystem struct{}

func (Filesystem) Scan(ctx context.Context, root string, visit func(catalog.File) error) (catalog.Coverage, error) {
	report := catalog.Coverage{Root: root, StartedAt: time.Now().UTC()}
	owners := ownerResolver{lookup: user.LookupId, names: make(map[int64]*string)}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			reportFailure(&report, path, walkErr)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			reportFailure(&report, path, err)
			return nil
		}
		kind := "other"
		switch {
		case info.Mode().IsRegular():
			kind = "file"
		case info.IsDir():
			kind = "directory"
		case info.Mode()&fs.ModeSymlink != 0:
			kind = "symlink"
		}
		uid := ownershipUID(info)
		return visit(catalog.File{
			Path: path, Name: filepath.Base(path), Kind: kind, Size: info.Size(),
			ModifiedAt: info.ModTime().UTC(), IndexedAt: time.Now().UTC(), Identity: identity(info),
			UID: uid, Owner: owners.resolve(uid),
		})
	})
	report.CompletedAt = time.Now().UTC()
	report.Complete = err == nil && report.ErrorCount == 0
	return report, err
}

func reportFailure(report *catalog.Coverage, path string, err error) {
	report.ErrorCount++
	// Keep the persisted diagnostics bounded even for large inaccessible trees.
	if len(report.Errors) < 100 {
		report.Errors = append(report.Errors, path+": "+err.Error())
	}
}
