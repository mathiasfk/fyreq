//go:build !darwin

package platform

import (
	"context"
	"fmt"
)

type Roots struct{}

func (Roots) DefaultRoots(context.Context) ([]string, error) {
	return nil, fmt.Errorf("default roots are not supported on this platform")
}
