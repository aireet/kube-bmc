package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/oob"
)

// PowerFunc executes a power action against a resolved target.
type PowerFunc func(ctx context.Context, t oob.Target, a bmcv1.PowerAction) error

// ActionReconciler runs in the server. It rejects actions while power actions are
// disabled, executes actions out-of-band that node agents cannot execute in-band (On,
// GracefulRestart, and in-band actions not claimed within ClaimTimeout because the node
// is down), and deletes finished actions after their TTL.
//
// Every executor claims an action with an optimistic-concurrency status update
// (Pending→Running), so each action is executed at most once.
type ActionReconciler struct {
	Client      client.Client
	Credentials Credentials
	Power       PowerFunc
	// Enabled is the master switch for power actions. When false, new actions are rejected.
	Enabled bool
	// ClaimTimeout is how long in-band actions are left to the node agent.
	ClaimTimeout time.Duration
	// Timeout bounds a single execution; Running actions older than twice this value are
	// marked Failed because the executing replica must have stopped.
	Timeout time.Duration
	// TTL is how long finished actions are kept. Zero keeps them forever.
	TTL time.Duration
	Now func() time.Time
}

func (r *ActionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&bmcv1.BMCAction{}).Named("bmcaction").Complete(r)
}

func (r *ActionReconciler) now() metav1.Time { return clock(r.Now) }

func clock(now func() time.Time) metav1.Time {
	if now != nil {
		return metav1.NewTime(now())
	}
	return metav1.Now()
}

func (r *ActionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	a := &bmcv1.BMCAction{}
	if err := r.Client.Get(ctx, req.NamespacedName, a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch {
	case a.Status.Phase.Done():
		return r.expire(ctx, a)
	case a.Status.Phase == bmcv1.PhaseRunning:
		if a.Status.StartTime != nil && r.now().Sub(a.Status.StartTime.Time) > 2*r.Timeout {
			return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseFailed, "execution was interrupted before completion")
		}
		return ctrl.Result{RequeueAfter: 2 * r.Timeout}, nil
	}

	if !r.Enabled {
		return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, "power actions are disabled on this kube-bmc server")
	}
	if InBand(a.Spec.Action) {
		if wait := a.CreationTimestamp.Add(r.ClaimTimeout).Sub(r.now().Time); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil // left to the node agent
		}
	}
	bmc := &bmcv1.BMC{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: a.Spec.BMCName}, bmc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, fmt.Sprintf("BMC %s not found", a.Spec.BMCName))
		}
		return ctrl.Result{}, err
	}
	creds, err := r.Credentials.For(ctx, bmc)
	if err != nil {
		reason := fmt.Sprintf("%s requires out-of-band access, which is not configured: %v", a.Spec.Action, err)
		if InBand(a.Spec.Action) {
			reason = fmt.Sprintf("the node agent on %s did not execute the action within %s and out-of-band access is not configured",
				a.Spec.BMCName, r.ClaimTimeout)
		}
		return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, reason)
	}
	target, err := oob.TargetFor(bmc, creds)
	if err != nil {
		return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, err.Error())
	}

	start := r.now()
	a.Status = bmcv1.BMCActionStatus{
		Phase:            bmcv1.PhaseRunning,
		Message:          fmt.Sprintf("sending %s to %s over %s", a.Spec.Action, target.Address, target.Protocol),
		PowerStateBefore: bmc.Status.PowerState,
		StartTime:        &start,
	}
	if err := r.Client.Status().Update(ctx, a); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // another replica claimed the action
		}
		return ctrl.Result{}, err
	}

	execCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	log.FromContext(ctx).Info("executing power action", "bmc", bmc.Name, "action", a.Spec.Action,
		"requestedBy", a.Spec.RequestedBy, "address", target.Address)
	if err := r.Power(execCtx, target, a.Spec.Action); err != nil {
		recordEvent(ctx, r.Client, a, bmc.Spec.NodeName, err, r.now())
		return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseFailed, err.Error())
	}
	recordEvent(ctx, r.Client, a, bmc.Spec.NodeName, nil, r.now())
	return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseSucceeded, fmt.Sprintf("%s accepted by the BMC", a.Spec.Action))
}

func (r *ActionReconciler) finish(ctx context.Context, a *bmcv1.BMCAction, phase bmcv1.ActionPhase, msg string) error {
	now := r.now()
	a.Status.Phase, a.Status.Message, a.Status.CompletionTime = phase, msg, &now
	if a.Status.StartTime == nil {
		a.Status.StartTime = &now
	}
	return r.Client.Status().Update(ctx, a)
}

func (r *ActionReconciler) expire(ctx context.Context, a *bmcv1.BMCAction) (ctrl.Result, error) {
	if r.TTL <= 0 || a.Status.CompletionTime == nil {
		return ctrl.Result{}, nil
	}
	left := a.Status.CompletionTime.Add(r.TTL).Sub(r.now().Time)
	if left > 0 {
		return ctrl.Result{RequeueAfter: left}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, a))
}

// recordEvent records a power action on the Node so it appears in `kubectl describe node`.
func recordEvent(ctx context.Context, c client.Client, a *bmcv1.BMCAction, node string, actionErr error, now metav1.Time) {
	typ, reason := corev1.EventTypeNormal, "BMCPowerAction"
	msg := fmt.Sprintf("%s requested by %s (BMCAction %s)", a.Spec.Action, a.Spec.RequestedBy, a.Name)
	if actionErr != nil {
		typ, reason = corev1.EventTypeWarning, "BMCPowerActionFailed"
		msg = fmt.Sprintf("%s requested by %s failed: %v (BMCAction %s)", a.Spec.Action, a.Spec.RequestedBy, actionErr, a.Name)
	}
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: node + ".", Namespace: metav1.NamespaceDefault},
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: node, APIVersion: "v1"},
		Reason:         reason,
		Message:        msg,
		Type:           typ,
		Source:         corev1.EventSource{Component: "kube-bmc"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if err := c.Create(ctx, ev); err != nil {
		log.FromContext(ctx).Error(err, "recording node event failed", "node", node)
	}
}
