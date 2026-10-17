// Package auth handles passwords, sessions, CSRF tokens, roles and the first admin.
package auth

import "context"

type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { _, ok := roleRank[r]; return ok }

// AtLeast reports whether r grants every permission of min. Unknown roles grant nothing.
func (r Role) AtLeast(min Role) bool {
	have, ok := roleRank[r]
	return ok && have >= roleRank[min]
}

// User is the signed-in principal attached to a request.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  Role   `json:"role"`

	tokenHash []byte
}

type ctxKey struct{}

// WithUser returns a context carrying u.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom returns the signed-in user, or nil.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}
