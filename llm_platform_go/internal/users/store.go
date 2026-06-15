// Package users defines the identity seam for the platform.
//
// The whole application depends only on the Store interface below — never on a
// concrete database. To move off the demo (in-memory) store onto a real
// identity backend (Postgres, an internal SSO/IdP, LDAP, …), implement Store
// and swap the single construction line in cmd/server/main.go. Nothing else
// changes, and the demo store persists nothing, so there is no data to migrate.
package users

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Store.GetByID when no user matches the id.
var ErrNotFound = errors.New("user not found")

// User is the minimal identity the UI needs. A real store may carry more
// fields internally; only these are exposed to the rest of the app.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// Store is the only contract the platform knows about its user database.
type Store interface {
	// GetByID returns the user with the given id, or ErrNotFound.
	GetByID(ctx context.Context, id string) (*User, error)
	// List returns all users. The demo login screen uses this to render the
	// one-click user buttons; a real SSO-backed store would typically not
	// expose this and the login handler would redirect to the IdP instead.
	List(ctx context.Context) ([]*User, error)
}
