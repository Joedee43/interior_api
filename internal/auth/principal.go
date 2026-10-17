package auth

import "context"

// Principal is who is making the current request.
type Principal struct {
	UserID    string
	SessionID string
}

// principalKey is an unexported type, so no other package can create a
// colliding context key or read the value without going through
// PrincipalFrom. (Using a plain string key like "user" is a classic bug:
// any package could overwrite it.)
type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the authenticated principal stored by
// RequireSession. It lives on context.Context, not *gin.Context, so
// services can read it too (Phase 3 authorization) without depending on Gin.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
