package platform

import (
	"errors"
	"os/user"
	"testing"
)

func TestOwnerResolutionCachesSuccessAndFailure(t *testing.T) {
	calls := make(map[string]int)
	resolver := ownerResolver{
		names: make(map[string]*string),
		lookup: func(uid string) (*user.User, error) {
			calls[uid]++
			if uid == "0" {
				return &user.User{Username: "root"}, nil
			}
			return nil, errors.New("account unavailable")
		},
	}
	root, unknown := "0", "12345"
	for range 2 {
		name := resolver.resolve(&root)
		if name == nil || *name != "root" {
			t.Fatalf("unexpected root owner: %v", name)
		}
		if resolver.resolve(&unknown) != nil || resolver.resolve(nil) != nil {
			t.Fatal("unavailable owner must remain nil")
		}
	}
	if calls["0"] != 1 || calls["12345"] != 1 || len(calls) != 2 {
		t.Fatalf("lookups were not cached: %v", calls)
	}
}
