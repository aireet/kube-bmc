package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func init() { hashCost = bcrypt.MinCost }

func newPasswords(t *testing.T, lines ...string) *Passwords {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-bmc-users", Namespace: "kube-bmc-system"},
		Data:       map[string][]byte{HtpasswdKey: []byte(strings.Join(lines, "\n"))},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s).Build()
	return &Passwords{Reader: c, Namespace: "kube-bmc-system", Secret: "kube-bmc-users"}
}

func mustHash(t *testing.T, user, password string) string {
	t.Helper()
	line, err := HashPassword(user, password)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestHashAndParseHtpasswd(t *testing.T) {
	line := mustHash(t, "alice", "correct horse")
	users, err := ParseHtpasswd("# operators\n\n" + line + "\n")
	if err != nil || len(users) != 1 || users["alice"] == nil {
		t.Fatalf("users = %v, err = %v", users, err)
	}
	// htpasswd -B writes $2y$ hashes.
	if _, err := ParseHtpasswd("bob:$2y$05$WhDpJ6nHDOJNQ7rJM0ApwOMGTeqVu6Y2VKw.Y7DeGKZ1TQ1d0LSk6"); err != nil {
		t.Fatalf("$2y$ hash rejected: %v", err)
	}
	for _, bad := range []string{"alice", ":hash", "alice:{SHA}W6ph5Mm5Pz8GgiULbPgzG37mj9g=", "alice:$apr1$abc$def"} {
		if _, err := ParseHtpasswd(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := HashPassword("a:b", "longenough"); err == nil {
		t.Error("username with ':' accepted")
	}
	if _, err := HashPassword("alice", "short"); err == nil {
		t.Error("short password accepted")
	}
}

func TestPasswordVerify(t *testing.T) {
	p := newPasswords(t, mustHash(t, "alice", "correct horse"))
	ctx := context.Background()

	id, err := p.Verify(ctx, "alice", "correct horse", "10.0.0.1")
	if err != nil || id.Username != "alice" || id.Method != MethodPassword {
		t.Fatalf("id = %+v, err = %v", id, err)
	}
	if _, err := p.Verify(ctx, "alice", "wrong", "10.0.0.1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := p.Verify(ctx, "mallory", "correct horse", "10.0.0.1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestPasswordLockout(t *testing.T) {
	p := newPasswords(t, mustHash(t, "alice", "correct horse"))
	now := time.Now()
	p.limiter.now = func() time.Time { return now }
	ctx := context.Background()

	for i := range freeAttempts {
		if _, err := p.Verify(ctx, "alice", "guess", "10.0.0.9"); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Locked out, even with the right password and from another address.
	if _, err := p.Verify(ctx, "alice", "correct horse", "10.0.0.8"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("expected lockout, got %v", err)
	}
	now = now.Add(baseLockout + time.Second)
	if _, err := p.Verify(ctx, "alice", "correct horse", "10.0.0.8"); err != nil {
		t.Fatalf("after lockout: %v", err)
	}
	if _, err := p.Verify(ctx, "alice", "guess", "10.0.0.8"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("successful sign-in did not reset the failure count")
	}
}

func TestPasswordLoginHTTP(t *testing.T) {
	a, err := New(Options{Passwords: newPasswords(t, mustHash(t, "alice", "correct horse")),
		SessionSecret: []byte(strings.Repeat("s", 32)), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode() != "password" || !a.Enabled() {
		t.Fatalf("mode = %s", a.Mode())
	}

	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest("GET", "/auth/login?rd=/servers/x", nil))
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/login?rd=%2Fservers%2Fx" {
		t.Fatalf("GET: %d %s", rec.Code, loc)
	}

	post := func(body, ctype string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		a.Login(rec, req)
		return rec
	}
	if rec := post(`{"username":"alice","password":"correct horse"}`, "application/x-www-form-urlencoded"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", rec.Code)
	}
	if rec := post(`{"username":"alice","password":"nope"}`, "application/json"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec = post(`{"username":"alice","password":"correct horse"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
	}

	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	id, err := a.Identify(req)
	if err != nil || id.Username != "alice" || id.Method != MethodPassword {
		t.Fatalf("session: %+v %v", id, err)
	}
}
