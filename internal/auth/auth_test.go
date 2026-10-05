package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// provider is an OpenID Connect provider that signs in a fixed user without a login form.
type provider struct {
	*httptest.Server
	key    *rsa.PrivateKey
	signer jose.Signer

	mu     sync.Mutex
	nonces map[string]string // code -> nonce
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{key: key, signer: signer, nonces: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.URL,
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"jwks_uri":                              p.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "PKCE required", http.StatusBadRequest)
			return
		}
		code := "code-" + q.Get("state")
		p.mu.Lock()
		p.nonces[code] = q.Get("nonce")
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") == "" {
			http.Error(w, "missing code_verifier", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		nonce := p.nonces[r.Form.Get("code")]
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "opaque", "token_type": "Bearer", "expires_in": 3600,
			"id_token": p.token(t, map[string]any{"aud": "kube-bmc", "nonce": nonce}),
		})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// token signs an ID token for alice, overriding default claims with extra.
func (p *provider) token(t *testing.T, extra map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": p.URL, "sub": "u-1", "aud": "kube-bmc",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"email": "alice@example.com", "email_verified": true, "name": "Alice", "groups": []string{"sre", "oncall"},
	}
	for k, v := range extra {
		claims[k] = v
	}
	raw, err := jwt.Signed(p.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newAuthenticator(t *testing.T, p *provider, tokens *TokenReviewer) *Authenticator {
	t.Helper()
	o, err := NewOIDC(context.Background(), OIDCConfig{
		IssuerURL: p.URL, ClientID: "kube-bmc", ClientSecret: "s", RedirectURL: "http://kube-bmc.test/auth/callback",
		UsernamePrefix: "oidc:", GroupsPrefix: "oidc:", ExtraAudiences: []string{"kube-bmc-cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{OIDC: o, Tokens: tokens, SessionSecret: []byte(strings.Repeat("s", 32)),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBrowserSignIn(t *testing.T) {
	p := newProvider(t)
	a := newAuthenticator(t, p, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", a.Login)
	mux.HandleFunc("/auth/callback", a.Callback)
	mux.Handle("/api/me", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		_ = json.NewEncoder(w).Encode(id)
	})))
	app := httptest.NewServer(mux)
	defer app.Close()
	a.opts.OIDC.oauth.RedirectURL = app.URL + "/auth/callback"

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	resp, err := c.Get(app.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: %d", resp.StatusCode)
	}

	resp, err = c.Get(app.URL + "/auth/login?rd=/servers/gpu-01")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Request.URL.Path != "/servers/gpu-01" {
		t.Fatalf("redirected to %s after sign-in", resp.Request.URL)
	}

	resp, err = c.Get(app.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var id Identity
	_ = json.NewDecoder(resp.Body).Decode(&id)
	if id.Username != "oidc:alice@example.com" || id.Method != MethodSession || id.Name != "Alice" ||
		strings.Join(id.Groups, ",") != "oidc:sre,oidc:oncall" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestCallbackRejectsForgedState(t *testing.T) {
	p := newProvider(t)
	a := newAuthenticator(t, p, nil)
	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest("GET", "/auth/login", nil))
	req := httptest.NewRequest("GET", "/auth/callback?code=x&state=forged", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	a.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged state: %d", rec.Code)
	}
}

func TestBearerOIDCToken(t *testing.T) {
	p := newProvider(t)
	a := newAuthenticator(t, p, nil)
	ctx := context.Background()

	id, _, err := a.VerifyBearer(ctx, p.token(t, nil))
	if err != nil || id.Username != "oidc:alice@example.com" || id.Method != MethodOIDCToken {
		t.Fatalf("id = %+v, err = %v", id, err)
	}
	if _, _, err := a.VerifyBearer(ctx, p.token(t, map[string]any{"aud": "kube-bmc-cli"})); err != nil {
		t.Fatalf("extra audience rejected: %v", err)
	}
	for name, claims := range map[string]map[string]any{
		"foreign audience": {"aud": "other-app"},
		"expired":          {"exp": time.Now().Add(-time.Minute).Unix()},
		"unverified email": {"email_verified": false},
	} {
		if _, _, err := a.VerifyBearer(ctx, p.token(t, claims)); err == nil {
			t.Errorf("%s: token accepted", name)
		}
	}
}

func TestBearerKubernetesToken(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	reviews := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			tr := obj.(*authenticationv1.TokenReview)
			reviews++
			if tr.Spec.Token == "sa-token" {
				tr.Status.Authenticated = true
				tr.Status.User = authenticationv1.UserInfo{Username: "system:serviceaccount:ops:agent", Groups: []string{"system:serviceaccounts"}}
			}
			return nil
		},
	}).Build()
	p := newProvider(t)
	a := newAuthenticator(t, p, &TokenReviewer{Client: c, TTL: time.Minute})

	for range 2 {
		id, _, err := a.VerifyBearer(context.Background(), "sa-token")
		if err != nil || id.Username != "system:serviceaccount:ops:agent" || id.Method != MethodKubernetes {
			t.Fatalf("id = %+v, err = %v", id, err)
		}
	}
	if reviews != 1 {
		t.Fatalf("token reviewed %d times; want cached", reviews)
	}
	if _, _, err := a.VerifyBearer(context.Background(), "bogus"); err == nil {
		t.Fatal("invalid token accepted")
	}
}

func TestDisabledIsAnonymous(t *testing.T) {
	a, _ := New(Options{})
	id, err := a.Identify(httptest.NewRequest("GET", "/", nil))
	if err != nil || id.Username != Anonymous.Username || id.Method != MethodNone {
		t.Fatalf("id = %+v, err = %v", id, err)
	}
}

func TestCookieCodec(t *testing.T) {
	c, err := newCookieCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := c.encode("a", Identity{Username: "bob"}, time.Hour)
	var id Identity
	if err := c.decode("a", v, &id); err != nil || id.Username != "bob" {
		t.Fatalf("round trip: %v %+v", err, id)
	}
	if c.decode("b", v, &id) == nil {
		t.Fatal("value accepted under another cookie name")
	}
	tampered := []byte(v)
	tampered[len(tampered)/2] ^= 1
	if c.decode("a", string(tampered), &id) == nil {
		t.Fatal("tampered value accepted")
	}
	expired, _ := c.encode("a", Identity{}, -time.Second)
	if c.decode("a", expired, &id) == nil {
		t.Fatal("expired value accepted")
	}
	if _, err := newCookieCodec([]byte("short")); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestSafeRedirect(t *testing.T) {
	for in, want := range map[string]string{
		"/servers/a?tab=sensors": "/servers/a?tab=sensors",
		"":                       "/",
		"https://evil.example":   "/",
		"//evil.example/x":       "/",
		"/\\evil.example":        "/",
	} {
		if got := safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRBACAuthorizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	calls := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			sar := obj.(*authorizationv1.SubjectAccessReview)
			calls++
			attrs := sar.Spec.ResourceAttributes
			sar.Status.Allowed = attrs.Verb == "list" && attrs.Resource == "bmcs" ||
				attrs.Resource == "bmcactions" && attrs.Verb == "create" && len(sar.Spec.Groups) > 0 && sar.Spec.Groups[0] == "oidc:sre"
			return nil
		},
	}).Build()
	r := &RBAC{Client: c, TTL: time.Minute}
	ctx := context.Background()
	sre := Identity{Username: "oidc:alice", Groups: []string{"oidc:sre"}}
	dev := Identity{Username: "oidc:bob", Groups: []string{"oidc:dev"}}

	check := func(id Identity, p Permission, want bool) {
		t.Helper()
		if got, err := r.Authorize(ctx, id, p); err != nil || got != want {
			t.Fatalf("%s %s: got %v (%v), want %v", id.Username, p, got, err, want)
		}
	}
	check(sre, Read, true)
	check(sre, Operate, true)
	check(dev, Read, true)
	check(dev, Operate, false)
	check(dev, Operate, false)
	if calls != 4 {
		t.Fatalf("%d access reviews; want 4 with caching", calls)
	}
}
