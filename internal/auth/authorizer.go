package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
)

// Permission is an operation in kube-bmc.
type Permission string

const (
	// Read covers BMC inventory, sensors, events and the action history.
	Read Permission = "read"
	// Operate covers requesting power actions.
	Operate Permission = "operate"
)

// Resource attributes checked for each permission.
var permissionAttributes = map[Permission]authorizationv1.ResourceAttributes{
	Read:    {Group: bmcv1.GroupVersion.Group, Resource: "bmcs", Verb: "list"},
	Operate: {Group: bmcv1.GroupVersion.Group, Resource: "bmcactions", Verb: "create"},
}

// Authorizer decides whether an identity holds a permission.
type Authorizer interface {
	Authorize(ctx context.Context, id Identity, p Permission) (bool, error)
}

// AllowAll grants every permission. It is used when authentication is disabled.
type AllowAll struct{}

func (AllowAll) Authorize(context.Context, Identity, Permission) (bool, error) { return true, nil }

// RBAC authorizes with SubjectAccessReviews and caches decisions for TTL.
type RBAC struct {
	Client client.Client
	TTL    time.Duration

	mu    sync.Mutex
	cache map[string]decision
}

type decision struct {
	allowed bool
	expires time.Time
}

func (r *RBAC) Authorize(ctx context.Context, id Identity, p Permission) (bool, error) {
	attrs, ok := permissionAttributes[p]
	if !ok {
		return false, fmt.Errorf("unknown permission %q", p)
	}
	key := string(p) + "\x00" + id.Username + "\x00" + strings.Join(id.Groups, "\x00")

	r.mu.Lock()
	if d, ok := r.cache[key]; ok && time.Now().Before(d.expires) {
		r.mu.Unlock()
		return d.allowed, nil
	}
	r.mu.Unlock()

	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:               id.Username,
			Groups:             id.Groups,
			ResourceAttributes: &attrs,
		},
	}
	if err := r.Client.Create(ctx, sar); err != nil {
		return false, fmt.Errorf("subject access review: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]decision{}
	}
	if len(r.cache) > 4096 {
		now := time.Now()
		for k, d := range r.cache {
			if now.After(d.expires) {
				delete(r.cache, k)
			}
		}
	}
	r.cache[key] = decision{allowed: sar.Status.Allowed, expires: time.Now().Add(r.TTL)}
	return sar.Status.Allowed, nil
}
