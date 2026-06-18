package users

import (
	"context"
	"sort"

	"llm_platform_go/internal/auth"
)

// DemoStore is an ephemeral, in-memory Store seeded with a fixed set of users.
//
// It writes nothing to disk: when the process exits the "database" is gone, so
// there is nothing to carry over to a real backend later. IDs are stable
// constants so that runs stamped with a user id stay consistent across
// restarts during development.
//
// This is the temporary database the platform uses to access and generate the
// (demo) SSO session. Replacing it with a real Store is the only change needed
// to point the platform at a production identity backend.
type DemoStore struct {
	byID  map[string]*User
	order []string // preserves seed order for List
}

// NewDemoStore returns a DemoStore seeded with the default demo users.
func NewDemoStore() *DemoStore {
	seed := []*User{
		{ID: "u-admin", Email: "admin@demo.local", Name: "Admin", Role: auth.RoleAdmin},
	}
	s := &DemoStore{byID: make(map[string]*User, len(seed))}
	for _, u := range seed {
		s.byID[u.ID] = u
		s.order = append(s.order, u.ID)
	}
	return s
}

func (s *DemoStore) GetByID(_ context.Context, id string) (*User, error) {
	u, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	// Return a copy so callers can't mutate the seed.
	cp := *u
	return &cp, nil
}

func (s *DemoStore) List(_ context.Context) ([]*User, error) {
	out := make([]*User, 0, len(s.order))
	for _, id := range s.order {
		cp := *s.byID[id]
		out = append(out, &cp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
