package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
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

var (
	// ErrUnauthenticated is returned when a request carries no valid credentials.
	ErrUnauthenticated = errors.New("authentication required")
	// ErrUnavailable is returned when credentials could not be checked, for example
	// because the Kubernetes API server is unreachable or reviews are rate limited. The
	// client should retry; its credentials may be valid.
	ErrUnavailable = errors.New("authentication temporarily unavailable")
)

// Options configures an Authenticator.
type Options struct {
	// OIDC enables browser sign-in and OIDC bearer tokens. Nil disables both.
	OIDC *OIDC
	// Passwords enables browser sign-in with a username and password. It is not used
	// together with OIDC.
	Passwords *Passwords
	// Tokens enables Kubernetes bearer tokens. Nil disables them.
	Tokens *TokenReviewer
	// SessionSecret encrypts session cookies. Required when OIDC or Passwords is set.
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
	if opts.OIDC != nil || opts.Passwords != nil {
		codec, err := newCookieCodec(opts.SessionSecret)
		if err != nil {
			return nil, err
		}
		a.codec = codec
	}
	return a, nil
}

// Enabled reports whether requests must authenticate.
func (a *Authenticator) Enabled() bool {
	return a.opts.OIDC != nil || a.opts.Passwords != nil || a.opts.Tokens != nil
}

// Mode returns the browser sign-in mode: "oidc", "password" or "none".
func (a *Authenticator) Mode() string {
	switch {
	case a.opts.OIDC != nil:
		return "oidc"
	case a.opts.Passwords != nil:
		return "password"
	}
	return "none"
}

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
	if a.opts.OIDC != nil || a.opts.Passwords != nil {
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
	if o := a.opts.OIDC; o != nil {
		if c, _ := unverifiedClaims(token); c.Issuer == o.Issuer() {
			id, tok, err := o.Verify(ctx, token)
			if err != nil {
				return Identity{}, time.Time{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
			}
			id.Method = MethodOIDCToken
			return id, tok.Expiry, nil
		}
	}
	if a.opts.Tokens != nil {
		id, err := a.opts.Tokens.Review(ctx, token)
		switch {
		case errors.Is(err, ErrUnavailable):
			return Identity{}, time.Time{}, err
		case err != nil:
			return Identity{}, time.Time{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
		}
		return id, time.Now().Add(a.opts.Tokens.TTL()), nil
	}
	return Identity{}, time.Time{}, ErrUnauthenticated
}

// MCPVerifier adapts VerifyBearer to the MCP SDK's bearer-token middleware.
func (a *Authenticator) MCPVerifier() mcpauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		id, exp, err := a.VerifyBearer(ctx, token)
		switch {
		case errors.Is(err, ErrUnavailable):
			return nil, err // a server error, not an invalid token
		case err != nil:
			return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, err)
		}
		return &mcpauth.TokenInfo{UserID: id.Username, Expiration: exp, Extra: map[string]any{"identity": id}}, nil
	}
}

// Middleware stores the request identity in the context, or responds 401.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Identify(r)
		switch {
		case errors.Is(err, ErrUnavailable):
			w.Header().Set("Retry-After", "1")
			writeAuthError(w, http.StatusServiceUnavailable, err.Error())
			return
		case err != nil:
			w.Header().Set("WWW-Authenticate", `Bearer realm="kube-bmc"`)
			writeAuthError(w, http.StatusUnauthorized, err.Error())
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

// Login signs a user in. With OIDC it starts the authorization code flow with PKCE;
// with passwords, GET redirects to the sign-in page and POST checks the credentials.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	if a.opts.Passwords != nil {
		a.passwordLogin(w, r)
		return
	}
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

type passwordLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *Authenticator) passwordLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/login?rd="+url.QueryEscape(safeRedirect(r.URL.Query().Get("rd"))), http.StatusFound)
		return
	}
	// A JSON body cannot be sent cross-site without a CORS preflight.
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeAuthError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var req passwordLoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	id, err := a.opts.Passwords.Verify(r.Context(), req.Username, req.Password, host)
	switch {
	case errors.Is(err, ErrTooManyAttempts):
		a.opts.Log.Warn("sign-in rejected", "user", req.Username, "client", host, "reason", "locked out")
		writeAuthError(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, ErrUnauthenticated):
		a.opts.Log.Warn("sign-in failed", "user", req.Username, "client", host)
		writeAuthError(w, http.StatusUnauthorized, "invalid username or password")
		return
	case err != nil:
		a.opts.Log.Error("sign-in failed", "err", err)
		writeAuthError(w, http.StatusInternalServerError, "sign-in is unavailable")
		return
	}
	value, err := a.codec.encode(sessionCookie, id, a.opts.SessionTTL)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.setCookie(w, sessionCookie, value, a.opts.SessionTTL)
	a.opts.Log.Info("user signed in", "user", id.Username, "method", id.Method)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(id)
}

func writeAuthError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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

// claims are the JWT claims used to choose how a token is verified.
type claims struct {
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`
}

// unverifiedClaims decodes the claims of a JWT without verifying its signature. The
// result only selects a verifier; it must not be trusted.
func unverifiedClaims(token string) (claims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, false
	}
	var c claims
	if json.Unmarshal(payload, &c) != nil {
		return claims{}, false
	}
	return c, true
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
