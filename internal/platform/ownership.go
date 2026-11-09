package platform

import (
	"os/user"
	"strconv"
)

// Cache successful and failed lookups within one scan. Account resolution is
// best-effort: a missing account name must not discard a valid numeric UID.
type ownerResolver struct {
	lookup func(string) (*user.User, error)
	names  map[int64]*string
}

func (r *ownerResolver) resolve(uid *int64) *string {
	if uid == nil {
		return nil
	}
	if name, ok := r.names[*uid]; ok {
		return name
	}
	var name *string
	account, err := r.lookup(strconv.FormatInt(*uid, 10))
	if err == nil && account != nil && account.Username != "" {
		name = &account.Username
	}
	r.names[*uid] = name
	return name
}
