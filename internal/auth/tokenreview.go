package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TokenReviewer authenticates Kubernetes bearer tokens (for example ServiceAccount
// tokens issued with `kubectl create token`) with the TokenReview API.
type TokenReviewer struct {
	Client client.Client
	TTL    time.Duration

	mu    sync.Mutex
	cache map[[32]byte]reviewed
}

type reviewed struct {
	id      Identity
	err     error
	expires time.Time
}

func (t *TokenReviewer) Review(ctx context.Context, token string) (Identity, error) {
	key := sha256.Sum256([]byte(token))
	t.mu.Lock()
	if r, ok := t.cache[key]; ok && time.Now().Before(r.expires) {
		t.mu.Unlock()
		return r.id, r.err
	}
	t.mu.Unlock()

	tr := &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token}}
	if err := t.Client.Create(ctx, tr); err != nil {
		return Identity{}, fmt.Errorf("token review: %w", err)
	}
	var id Identity
	var err error
	if tr.Status.Authenticated {
		id = Identity{Username: tr.Status.User.Username, Groups: tr.Status.User.Groups, Method: MethodKubernetes}
	} else {
		err = errors.New("token is not valid for the Kubernetes API server")
		if tr.Status.Error != "" {
			err = errors.New(tr.Status.Error)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		t.cache = map[[32]byte]reviewed{}
	}
	if len(t.cache) > 4096 {
		now := time.Now()
		for k, r := range t.cache {
			if now.After(r.expires) {
				delete(t.cache, k)
			}
		}
	}
	t.cache[key] = reviewed{id: id, err: err, expires: time.Now().Add(t.TTL)}
	return id, err
}
