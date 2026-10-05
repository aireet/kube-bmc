package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "kube_bmc_session"
	loginCookie   = "kube_bmc_login"
	loginTTL      = 10 * time.Minute
)

// ErrUnauthenticated is returned when a request carries no valid credentials.
var ErrUnauthenticated = errors.New("authentication required")

// Options configures an Authenticator.
type Options struct {
	// OIDC enables browser sign-in and OIDC bearer tokens. Nil disables both.
	OIDC *OIDC
	// Tokens enables Kubernetes bearer tokens. Nil disables them.
	Tokens *TokenReviewer
	// SessionSecret encrypts session cookies. Required when OIDC is set.
	SessionSecret []byte
	SessionTTL    time.Duration
	// SecureCookies sets the Secure attribute; enable it when served over HTTPS.
	SecureCookies bool
	Log           *slog.Logger
}

// Authenticator establishes the identity of HTTP requests.
type Authenticator struct {
	opts  Options
	codec cookieCodec
}

// New returns an Authenticator. With neither OIDC nor Tokens configured, every
// request is treated as Anonymous.
func New(opts Options) (*Authenticator, error) {
	a := &Authenticator{opts: opts}
	if a.opts.Log == nil {
		a.opts.Log = slog.Default()
	}
	if opts.SessionTTL <= 0 {
		a.opts.SessionTTL = 12 * time.Hour
	}
	if opts.OIDC != nil {
		codec, err := newCookieCodec(opts.SessionSecret)
		if err != nil {
			return nil, err
		}
		a.codec = codec
	}
	return a, nil
}

// Enabled reports whether requests must authenticate.
func (a *Authenticator) Enabled() bool { return a.opts.OIDC != nil || a.opts.Tokens != nil }

// LoginEnabled reports whether browser sign-in is available.
func (a *Authenticator) LoginEnabled() bool { return a.opts.OIDC != nil }

// Identify returns the identity of r from its bearer token or session cookie.
func (a *Authenticator) Identify(r *http.Request) (Identity, error) {
	if !a.Enabled() {
		return Anonymous, nil
	}
	if h := r.Header.Get("Authorization"); h != "" {
		token, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || token == "" {
			return Identity{}, ErrUnauthenticated
		}
		id, _, err := a.VerifyBearer(r.Context(), token)
		return id, err
	}
	if a.opts.OIDC != nil {
		if c, err := r.Cookie(sessionCookie); err == nil {
			var id Identity
			if err := a.codec.decode(sessionCookie, c.Value, &id); err == nil {
				return id, nil
			}
		}
	}
	return Identity{}, ErrUnauthenticated
}

// VerifyBearer authenticates a bearer token. Tokens whose issuer matches the OIDC
// provider are verified locally; all others are sent to the TokenReview API.
// The returned time is the token expiry, if known.
func (a *Authenticator) VerifyBearer(ctx context.Context, token string) (Identity, time.Time, error) {
	if o := a.opts.OIDC; o != nil && unverifiedIssuer(token) == o.Issuer() {
		id, tok, err := o.Verify(ctx, token)
		if err != nil {
			return Identity{}, time.Time{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
		}
		id.Method = MethodOIDCToken
		return id, tok.Expiry, nil
	}
	if a.opts.Tokens != nil {
		id, err := a.opts.Tokens.Review(ctx, token)
		if err != nil {
			return Identity{}, time.Time{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
		}
		return id, time.Now().Add(a.opts.Tokens.TTL), nil
	}
	return Identity{}, time.Time{}, ErrUnauthenticated
}

// MCPVerifier adapts VerifyBearer to the MCP SDK's bearer-token middleware.
func (a *Authenticator) MCPVerifier() mcpauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		id, exp, err := a.VerifyBearer(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, err)
		}
		return &mcpauth.TokenInfo{UserID: id.Username, Expiration: exp, Extra: map[string]any{"identity": id}}, nil
	}
}

// Middleware stores the request identity in the context, or responds 401.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Identify(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="kube-bmc"`)
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

type loginState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	Redirect string `json:"r"`
}

// Login starts the authorization code flow with PKCE.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	o := a.opts.OIDC
	if o == nil {
		http.NotFound(w, r)
		return
	}
	st := loginState{State: randomString(), Verifier: oauth2.GenerateVerifier(), Nonce: randomString(),
		Redirect: safeRedirect(r.URL.Query().Get("rd"))}
	value, err := a.codec.encode(loginCookie, st, loginTTL)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	a.setCookie(w, loginCookie, value, loginTTL)
	http.Redirect(w, r, o.oauth.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier), oidc.Nonce(st.Nonce)), http.StatusFound)
}

// Callback completes the authorization code flow and establishes a session.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	o := a.opts.OIDC
	if o == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		a.fail(w, http.StatusForbidden, "sign-in failed: "+e+" "+q.Get("error_description"), nil)
		return
	}
	var st loginState
	c, err := r.Cookie(loginCookie)
	if err != nil || a.codec.decode(loginCookie, c.Value, &st) != nil {
		a.fail(w, http.StatusBadRequest, "sign-in expired; start again", err)
		return
	}
	a.setCookie(w, loginCookie, "", -1)
	if q.Get("state") != st.State {
		a.fail(w, http.StatusBadRequest, "state mismatch", nil)
		return
	}
	tok, err := o.oauth.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		a.fail(w, http.StatusBadGateway, "token exchange failed", err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		a.fail(w, http.StatusBadGateway, "provider returned no ID token", nil)
		return
	}
	id, idt, err := o.Verify(r.Context(), raw)
	if err != nil {
		a.fail(w, http.StatusForbidden, "ID token rejected", err)
		return
	}
	if idt.Nonce != st.Nonce {
		a.fail(w, http.StatusForbidden, "nonce mismatch", nil)
		return
	}
	id.Method = MethodSession
	value, err := a.codec.encode(sessionCookie, id, a.opts.SessionTTL)
	if err != nil {
		a.fail(w, http.StatusInternalServerError, "internal error", err)
		return
	}
	a.setCookie(w, sessionCookie, value, a.opts.SessionTTL)
	a.opts.Log.Info("user signed in", "user", id.Username, "groups", id.Groups)
	http.Redirect(w, r, st.Redirect, http.StatusFound)
}

// Logout clears the session.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	a.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/?signed_out=1", http.StatusFound)
}

func (a *Authenticator) fail(w http.ResponseWriter, code int, msg string, err error) {
	if err != nil {
		a.opts.Log.Warn("sign-in failed", "reason", msg, "err", err)
	}
	http.Error(w, msg, code)
}

func (a *Authenticator) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.opts.SecureCookies, SameSite: http.SameSiteLaxMode}
	if ttl < 0 {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(ttl.Seconds())
	}
	http.SetCookie(w, c)
}

// safeRedirect allows only local absolute paths, preventing open redirects.
func safeRedirect(rd string) string {
	if !strings.HasPrefix(rd, "/") || strings.HasPrefix(rd, "//") || strings.ContainsAny(rd, "\\\r\n") {
		return "/"
	}
	if u, err := url.Parse(rd); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return rd
}

// unverifiedIssuer reads the iss claim of a JWT without verifying it, to choose a verifier.
func unverifiedIssuer(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Issuer
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
