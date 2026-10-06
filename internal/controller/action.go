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
	"github.com/aireet/kube-bmc/bmc"
)

// ActionReconciler runs in the server. It rejects actions while actions are disabled,
// executes power actions out-of-band that node agents cannot execute in-band (On,
// GracefulRestart, and in-band power actions not claimed within ClaimTimeout because the
// node is down), and deletes finished actions after their TTL.
//
// Every executor claims an action with an optimistic-concurrency status update
// (Pending→Running), so each action is executed at most once.
type ActionReconciler struct {
	Client    client.Client
	Endpoints Endpoints
	// Power performs power actions; bmc.Power in production.
	Power func(ctx context.Context, e bmc.Endpoint, a bmc.PowerAction) error
	// Enabled is the master switch for actions. When false, new actions are rejected.
	Enabled bool
	// ClaimTimeout is how long in-band actions are left to the node agent.
	ClaimTimeout time.Duration
	// Timeout bounds a single out-of-band execution and sets its deadline.
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
		return r.watch(ctx, a)
	}

	if !r.Enabled {
		return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, "actions are disabled on this kube-bmc server")
	}
	if InBand(a.Spec.Action) {
		if wait := a.CreationTimestamp.Add(r.ClaimTimeout).Sub(r.now().Time); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil // left to the node agent
		}
		if !a.Spec.Action.IsPower() {
			return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, fmt.Sprintf(
				"the node agent on %s did not execute the action within %s; %s is only available in-band",
				a.Spec.BMCName, r.ClaimTimeout, a.Spec.Action))
		}
	}
	b := &bmcv1.BMC{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: a.Spec.BMCName}, b); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, fmt.Sprintf("BMC %s not found", a.Spec.BMCName))
		}
		return ctrl.Result{}, err
	}
	endpoint, err := r.Endpoints.For(ctx, b)
	if err != nil {
		reason := fmt.Sprintf("%s requires out-of-band access, which is not configured: %v", a.Spec.Action, err)
		if InBand(a.Spec.Action) {
			reason = fmt.Sprintf("the node agent on %s did not execute the action within %s and out-of-band access is not configured: %v",
				a.Spec.BMCName, r.ClaimTimeout, err)
		}
		return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseRejected, reason)
	}

	start := r.now()
	a.Status = bmcv1.BMCActionStatus{
		Phase:            bmcv1.PhaseRunning,
		Message:          fmt.Sprintf("sending %s to %s over %s", a.Spec.Action, endpoint.Address, protocol(endpoint)),
		PowerStateBefore: b.Status.PowerState,
		StartTime:        &start,
		Deadline:         &metav1.Time{Time: start.Add(r.Timeout)},
	}
	if err := r.Client.Status().Update(ctx, a); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // another replica claimed the action
		}
		return ctrl.Result{}, err
	}

	execCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	log.FromContext(ctx).Info("executing power action", "bmc", b.Name, "action", a.Spec.Action,
		"requestedBy", a.Spec.RequestedBy, "address", endpoint.Address)
	if err := r.Power(execCtx, endpoint, bmc.PowerAction(a.Spec.Action)); err != nil {
		recordEvent(ctx, r.Client, a, b.Spec.NodeName, err, r.now())
		return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseFailed, err.Error())
	}
	recordEvent(ctx, r.Client, a, b.Spec.NodeName, nil, r.now())
	return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseSucceeded, fmt.Sprintf("%s accepted by the BMC", a.Spec.Action))
}

func protocol(e bmc.Endpoint) bmc.Protocol {
	if e.Protocol == "" {
		return bmc.Redfish
	}
	return e.Protocol
}

// deadlineGrace is how long after its deadline an executor has to record the outcome.
const deadlineGrace = time.Minute

// watch fails a Running action whose executor did not record an outcome by the deadline
// the executor set, whichever component that is.
func (r *ActionReconciler) watch(ctx context.Context, a *bmcv1.BMCAction) (ctrl.Result, error) {
	var deadline time.Time
	switch {
	case a.Status.Deadline != nil:
		deadline = a.Status.Deadline.Time
	case a.Status.StartTime != nil:
		// Started by a version that did not record deadlines.
		deadline = a.Status.StartTime.Add(2 * r.Timeout)
	default:
		deadline = a.CreationTimestamp.Add(2 * r.Timeout)
	}
	if wait := deadline.Add(deadlineGrace).Sub(r.now().Time); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	return ctrl.Result{}, r.finish(ctx, a, bmcv1.PhaseFailed, fmt.Sprintf(
		"execution was interrupted: no outcome was recorded by the deadline %s", deadline.UTC().Format(time.RFC3339)))
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
