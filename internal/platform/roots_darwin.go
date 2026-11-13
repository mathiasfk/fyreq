package platform

import "context"

type Roots struct{}

func (Roots) DefaultRoots(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []string{"/"}, nil
}
