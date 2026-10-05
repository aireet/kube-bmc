// Package auth authenticates dashboard, API and MCP requests.
//
// Identities come from an OIDC browser session, an OIDC bearer token, or a Kubernetes
// bearer token (TokenReview). Every authenticated identity has full access; the identity
// is recorded as the requester of power actions.
package auth

import "context"

// Method is how an identity was established.
type Method string

const (
	MethodNone       Method = "none" // authentication is disabled
	MethodSession    Method = "session"
	MethodOIDCToken  Method = "oidc-token"
	MethodKubernetes Method = "kubernetes"
)

// Identity is an authenticated principal. Username and Groups are expressed in
// Kubernetes terms (OIDC claims carry the configured prefixes).
type Identity struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups,omitempty"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Method   Method   `json:"method"`
}

// Anonymous is the identity used when authentication is disabled.
var Anonymous = Identity{Username: "anonymous", Method: MethodNone}

type identityKey struct{}

// WithIdentity returns a context carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext returns the identity stored in ctx.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
