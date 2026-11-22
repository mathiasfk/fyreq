//go:build !darwin && !linux

package storage

import (
	"context"
	"testing"

	"fyreq/internal/app"
)

func TestUnsupportedMutationDoesNotInvokeCallback(t *testing.T) {
	called := false
	result, err := (Store{Dir: t.TempDir()}).Update(context.Background(), func(app.Transaction) error { called = true; return nil })
	if err == nil || called || result.Published {
		t.Fatalf("result=%#v error=%v called=%v", result, err, called)
	}
}
