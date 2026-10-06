package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/utils/lru"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TokenReviewer authenticates Kubernetes ServiceAccount tokens with the TokenReview API.
// Only ServiceAccounts in its namespace are accepted, so that tokens of arbitrary
// workloads in the cluster do not grant access.
//
// Bearer tokens come from unauthenticated clients, so the work a client can cause is
// bounded. A token is sent to the API server only if it is a JWT whose unverified subject
// is a ServiceAccount of the namespace. Results, rejections included, are kept in an LRU
// cache of fixed size; concurrent reviews of one token are coalesced; and reviews are
// rate limited.
type TokenReviewer struct {
	client    client.Client
	namespace string
	ttl       time.Duration
	cache     *lru.Cache
	flight    singleflight.Group
	limiter   *rate.Limiter
	now       func() time.Time
}

// TokenReviewOptions tunes a TokenReviewer. Zero values select the defaults.
type TokenReviewOptions struct {
	// TTL is how long a review result is used. Default one minute.
	TTL time.Duration
	// CacheSize is the maximum number of cached results. Default 4096.
	CacheSize int
	// Rate and Burst limit the reviews sent to the API server. Default 20 per second
	// with a burst of 40.
	Rate  rate.Limit
	Burst int
}

// reviewTimeout bounds a TokenReview request, which is shared by coalesced callers.
const reviewTimeout = 10 * time.Second

func NewTokenReviewer(c client.Client, namespace string, opts TokenReviewOptions) *TokenReviewer {
	if opts.TTL <= 0 {
		opts.TTL = time.Minute
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = 4096
	}
	if opts.Rate <= 0 {
		opts.Rate, opts.Burst = 20, 40
	}
	return &TokenReviewer{
		client: c, namespace: namespace, ttl: opts.TTL,
		cache:   lru.New(opts.CacheSize),
		limiter: rate.NewLimiter(opts.Rate, max(opts.Burst, 1)),
		now:     time.Now,
	}
}

type reviewed struct {
	id      Identity
	err     error
	expires time.Time
}

// Review authenticates token. It returns an error wrapping ErrUnavailable when the
// token could not be checked, and any other error when the token is not accepted.
func (t *TokenReviewer) Review(ctx context.Context, token string) (Identity, error) {
	if c, ok := unverifiedClaims(token); !ok || !strings.HasPrefix(c.Subject, t.prefix()) {
		return Identity{}, fmt.Errorf("not a token of a ServiceAccount in namespace %s", t.namespace)
	}
	sum := sha256.Sum256([]byte(token))
	key := string(sum[:])
	if r, ok := t.cached(key); ok {
		return r.id, r.err
	}
	v, err, _ := t.flight.Do(key, func() (any, error) {
		if r, ok := t.cached(key); ok {
			return r, nil
		}
		if !t.limiter.Allow() {
			return nil, fmt.Errorf("%w: too many token reviews", ErrUnavailable)
		}
		// The review is shared by every caller waiting for it, so it must not depend on
		// the first caller's cancellation.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reviewTimeout)
		defer cancel()
		r, err := t.review(rctx, token)
		if err != nil {
			return nil, err
		}
		t.cache.Add(key, r)
		return r, nil
	})
	if err != nil {
		return Identity{}, err
	}
	r := v.(reviewed)
	return r.id, r.err
}

func (t *TokenReviewer) prefix() string { return "system:serviceaccount:" + t.namespace + ":" }

func (t *TokenReviewer) cached(key string) (reviewed, bool) {
	v, ok := t.cache.Get(key)
	if !ok {
		return reviewed{}, false
	}
	r := v.(reviewed)
	if !t.now().Before(r.expires) {
		t.cache.Remove(key)
		return reviewed{}, false
	}
	return r, true
}

// review asks the API server. An error means the token could not be checked; the
// outcome of a completed review is returned in reviewed.err.
func (t *TokenReviewer) review(ctx context.Context, token string) (reviewed, error) {
	tr := &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token}}
	if err := t.client.Create(ctx, tr); err != nil {
		return reviewed{}, fmt.Errorf("%w: token review: %w", ErrUnavailable, err)
	}
	r := reviewed{expires: t.now().Add(t.ttl)}
	switch {
	case tr.Status.Authenticated && strings.HasPrefix(tr.Status.User.Username, t.prefix()):
		r.id = Identity{Username: tr.Status.User.Username, Groups: tr.Status.User.Groups, Method: MethodKubernetes}
	case tr.Status.Authenticated:
		r.err = fmt.Errorf("%s is not a ServiceAccount in namespace %s", tr.Status.User.Username, t.namespace)
	case tr.Status.Error != "":
		r.err = errors.New(tr.Status.Error)
	default:
		r.err = errors.New("token is not valid for the Kubernetes API server")
	}
	return r, nil
}

// TTL is how long a review result is used.
func (t *TokenReviewer) TTL() time.Duration { return t.ttl }
