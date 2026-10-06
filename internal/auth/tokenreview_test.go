package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// saToken returns an unsigned JWT with the subject of a ServiceAccount; n makes it unique.
func saToken(namespace, name string, n int) string {
	payload, _ := json.Marshal(map[string]any{"sub": "system:serviceaccount:" + namespace + ":" + name, "jti": n})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln"
}

// reviewClient answers TokenReviews: tokens in users authenticate as the mapped user,
// all others are rejected. It counts the reviews.
func reviewClient(t *testing.T, users map[string]string) (client.Client, *atomic.Int64) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	var reviews atomic.Int64
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			reviews.Add(1)
			tr := obj.(*authenticationv1.TokenReview)
			if u, ok := users[tr.Spec.Token]; ok {
				tr.Status.Authenticated = true
				tr.Status.User = authenticationv1.UserInfo{Username: u}
			}
			return nil
		},
	}).Build()
	return c, &reviews
}

func TestTokenReviewCacheIsBounded(t *testing.T) {
	c, reviews := reviewClient(t, nil)
	r := NewTokenReviewer(c, "ns", TokenReviewOptions{CacheSize: 2})
	for i := range 3 {
		_, _ = r.Review(t.Context(), saToken("ns", "x", i))
	}
	if r.cache.Len() != 2 {
		t.Fatalf("cache holds %d results", r.cache.Len())
	}
	_, _ = r.Review(t.Context(), saToken("ns", "x", 0)) // evicted
	_, _ = r.Review(t.Context(), saToken("ns", "x", 2)) // cached, rejection included
	if reviews.Load() != 4 {
		t.Fatalf("%d reviews", reviews.Load())
	}
}

func TestTokenReviewExpires(t *testing.T) {
	token := saToken("ns", "agent", 0)
	c, reviews := reviewClient(t, map[string]string{token: "system:serviceaccount:ns:agent"})
	r := NewTokenReviewer(c, "ns", TokenReviewOptions{TTL: time.Minute})
	now := time.Now()
	r.now = func() time.Time { return now }
	_, _ = r.Review(t.Context(), token)
	now = now.Add(time.Minute)
	if _, err := r.Review(t.Context(), token); err != nil || reviews.Load() != 2 {
		t.Fatalf("err = %v, %d reviews", err, reviews.Load())
	}
}

func TestTokenReviewCoalescesConcurrentRequests(t *testing.T) {
	token := saToken("ns", "agent", 0)
	release := make(chan struct{})
	var reviews atomic.Int64
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			reviews.Add(1)
			<-release
			tr := obj.(*authenticationv1.TokenReview)
			tr.Status.Authenticated, tr.Status.User.Username = true, "system:serviceaccount:ns:agent"
			return nil
		},
	}).Build()
	r := NewTokenReviewer(c, "ns", TokenReviewOptions{})
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := r.Review(t.Context(), token); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if reviews.Load() != 1 {
		t.Fatalf("%d reviews for one token", reviews.Load())
	}
}

func TestTokenReviewRateLimit(t *testing.T) {
	c, reviews := reviewClient(t, nil)
	r := NewTokenReviewer(c, "ns", TokenReviewOptions{Rate: 0.001, Burst: 1})
	_, _ = r.Review(t.Context(), saToken("ns", "x", 0))
	_, err := r.Review(t.Context(), saToken("ns", "x", 1))
	if !errors.Is(err, ErrUnavailable) || reviews.Load() != 1 {
		t.Fatalf("err = %v, %d reviews", err, reviews.Load())
	}
}

// A token that cannot be checked is a temporary server condition, not a bad credential.
func TestUnavailableIsNotUnauthorized(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return errors.New("connection refused")
		},
	}).Build()
	a := newAuthenticator(t, newProvider(t), NewTokenReviewer(c, "ns", TokenReviewOptions{}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bmcs", nil)
	req.Header.Set("Authorization", "Bearer "+saToken("ns", "agent", 0))
	w := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("request passed") })).ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, headers %v", w.Code, w.Header())
	}
}
