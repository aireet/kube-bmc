package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig configures the OpenID Connect provider.
type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// UsernameClaim and GroupsClaim select the ID token claims mapped to the Kubernetes
	// username and groups, mirroring the API server's --oidc-* flags.
	UsernameClaim  string
	GroupsClaim    string
	UsernamePrefix string
	GroupsPrefix   string
	// ExtraAudiences are accepted in bearer tokens in addition to ClientID, for tokens
	// issued to other clients such as a CLI or an MCP client.
	ExtraAudiences []string
}

// OIDC verifies ID tokens and drives the authorization code flow.
type OIDC struct {
	cfg       OIDCConfig
	oauth     oauth2.Config
	verifier  *oidc.IDTokenVerifier
	audiences []string
	// EndSessionURL is the provider's RP-initiated logout endpoint, if advertised.
	EndSessionURL string
}

// NewOIDC discovers the provider configuration from the issuer.
func NewOIDC(ctx context.Context, cfg OIDCConfig) (*OIDC, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return nil, errors.New("oidc: issuer URL and client ID are required")
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	var meta struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	_ = provider.Claims(&meta)

	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = "email"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email", "groups"}
	}
	return &OIDC{
		cfg: cfg,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		// The audience is checked against ExtraAudiences in Verify.
		verifier:      provider.Verifier(&oidc.Config{SkipClientIDCheck: true}),
		audiences:     append([]string{cfg.ClientID}, cfg.ExtraAudiences...),
		EndSessionURL: meta.EndSessionEndpoint,
	}, nil
}

// Issuer returns the configured issuer URL.
func (o *OIDC) Issuer() string { return o.cfg.IssuerURL }

// Verify validates a raw ID token and maps its claims to an identity.
func (o *OIDC) Verify(ctx context.Context, raw string) (Identity, *oidc.IDToken, error) {
	tok, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, nil, err
	}
	if !slices.ContainsFunc(tok.Audience, func(a string) bool { return slices.Contains(o.audiences, a) }) {
		return Identity{}, nil, fmt.Errorf("token audience %v is not accepted", tok.Audience)
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return Identity{}, nil, err
	}
	id, err := o.identity(claims)
	return id, tok, err
}

func (o *OIDC) identity(claims map[string]any) (Identity, error) {
	user, _ := claims[o.cfg.UsernameClaim].(string)
	if user == "" {
		return Identity{}, fmt.Errorf("token has no %q claim", o.cfg.UsernameClaim)
	}
	if o.cfg.UsernameClaim == "email" {
		if verified, ok := claims["email_verified"].(bool); ok && !verified {
			return Identity{}, errors.New("email is not verified")
		}
	}
	id := Identity{Username: o.cfg.UsernamePrefix + user}
	id.Email, _ = claims["email"].(string)
	id.Name, _ = claims["name"].(string)
	switch g := claims[o.cfg.GroupsClaim].(type) {
	case []any:
		for _, v := range g {
			if s, ok := v.(string); ok {
				id.Groups = append(id.Groups, o.cfg.GroupsPrefix+s)
			}
		}
	case string:
		for _, s := range strings.Split(g, ",") {
			if s = strings.TrimSpace(s); s != "" {
				id.Groups = append(id.Groups, o.cfg.GroupsPrefix+s)
			}
		}
	}
	return id, nil
}
