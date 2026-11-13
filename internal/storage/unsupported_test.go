//go:build !darwin

package storage

import (
	"context"
	"testing"

	"fyreq/internal/catalog"
)

func TestUnsupportedMutationDoesNotInvokeCallback(t *testing.T) {
	called := false
	result, err := (Store{Dir: t.TempDir()}).Update(context.Background(), func(*catalog.Snapshot) error { called = true; return nil })
	if err == nil || called || result.Published {
		t.Fatalf("result=%#v error=%v called=%v", result, err, called)
	}
}
