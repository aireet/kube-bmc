package agent

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LeaseLabel marks agent heartbeat Leases.
const LeaseLabel = "bmc.kube-bmc.io/agent-heartbeat"

// heartbeat renews a Lease named after the node in the agent namespace.
type heartbeat struct {
	k8s       client.Client
	namespace string
	node      string
	duration  time.Duration

	lease *coordinationv1.Lease
}

func (h *heartbeat) renew(ctx context.Context, node func(context.Context) (*corev1.Node, error)) error {
	now := metav1.NewMicroTime(time.Now())
	if h.lease == nil {
		l := &coordinationv1.Lease{}
		err := h.k8s.Get(ctx, client.ObjectKey{Namespace: h.namespace, Name: h.node}, l)
		switch {
		case apierrors.IsNotFound(err):
			n, err := node(ctx)
			if err != nil {
				return err
			}
			l = &coordinationv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      h.node,
					Namespace: h.namespace,
					Labels:    map[string]string{LeaseLabel: "true", "app.kubernetes.io/managed-by": "kube-bmc"},
					// Garbage-collected together with the Node, like the BMC object.
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: n.Name, UID: n.UID}},
				},
				Spec: coordinationv1.LeaseSpec{
					HolderIdentity:       ptr.To(h.node),
					LeaseDurationSeconds: ptr.To(int32(h.duration.Seconds())),
					AcquireTime:          &now,
					RenewTime:            &now,
				},
			}
			if err := h.k8s.Create(ctx, l); err != nil {
				return fmt.Errorf("create lease: %w", err)
			}
			h.lease = l
			return nil
		case err != nil:
			return fmt.Errorf("get lease: %w", err)
		}
		h.lease = l
	}

	l := h.lease.DeepCopy()
	l.Spec.RenewTime = &now
	l.Spec.LeaseDurationSeconds = ptr.To(int32(h.duration.Seconds()))
	if err := h.k8s.Update(ctx, l); err != nil {
		h.lease = nil // refetch on the next renewal
		return fmt.Errorf("renew lease: %w", err)
	}
	h.lease = l
	return nil
}

// LeaseExpired reports whether the heartbeat in l has expired at now. A nil Lease is expired.
func LeaseExpired(l *coordinationv1.Lease, now time.Time) bool {
	if l == nil || l.Spec.RenewTime == nil || l.Spec.LeaseDurationSeconds == nil {
		return true
	}
	return now.After(l.Spec.RenewTime.Add(time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second))
}
