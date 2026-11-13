package platform

import (
	"os/user"
)

// Cache successful and failed lookups within one scan. Account resolution is
// best-effort: a missing account name must not discard a valid native owner ID.
type ownerResolver struct {
	lookup func(string) (*user.User, error)
	names  map[string]*string
}

func (r *ownerResolver) resolve(ownerID *string) *string {
	if ownerID == nil {
		return nil
	}
	if name, ok := r.names[*ownerID]; ok {
		return name
	}
	var name *string
	account, err := r.lookup(*ownerID)
	if err == nil && account != nil && account.Username != "" {
		name = &account.Username
	}
	r.names[*ownerID] = name
	return name
}
