package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

// inBandVerbs maps the actions a node agent executes through the local BMC interface to
// `ipmitool chassis power` sub-commands. On cannot be executed in-band because the agent
// does not run while the host is off; GracefulRestart has no IPMI equivalent.
var inBandVerbs = map[bmcv1.PowerAction]string{
	bmcv1.ActionGracefulShutdown: "soft",
	bmcv1.ActionForceOff:         "off",
	bmcv1.ActionForceRestart:     "reset",
	bmcv1.ActionPowerCycle:       "cycle",
}

// InBand reports whether a node agent executes a through the local BMC interface.
func InBand(a bmcv1.PowerAction) bool {
	_, ok := inBandVerbs[a]
	return ok
}

// InBandReconciler runs in the node agent and executes the in-band actions for its node
// through /dev/ipmi0. No BMC credentials or network access to the BMC are needed.
type InBandReconciler struct {
	Client client.Client
	Node   string
	Runner ipmi.Runner
	// Enabled must also be set on the server; when false, the agent leaves actions alone
	// and the server rejects them.
	Enabled bool
	Timeout time.Duration
	Now     func() time.Time
}

func (r *InBandReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mine := predicate.NewPredicateFuncs(func(o client.Object) bool {
		a, ok := o.(*bmcv1.BMCAction)
		return ok && a.Spec.BMCName == r.Node && a.Status.Phase == "" && InBand(a.Spec.Action)
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&bmcv1.BMCAction{}, builder.WithPredicates(mine)).
		Named("bmcaction-inband").
		Complete(r)
}

func (r *InBandReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !r.Enabled {
		return ctrl.Result{}, nil
	}
	a := &bmcv1.BMCAction{}
	if err := r.Client.Get(ctx, req.NamespacedName, a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	verb, ok := inBandVerbs[a.Spec.Action]
	if !ok || a.Spec.BMCName != r.Node || a.Status.Phase != "" {
		return ctrl.Result{}, nil
	}
	bmc := &bmcv1.BMC{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.Node}, bmc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := clock(r.Now)
	a.Status = bmcv1.BMCActionStatus{
		Phase:            bmcv1.PhaseRunning,
		Message:          fmt.Sprintf("executing %s in-band on %s", a.Spec.Action, r.Node),
		PowerStateBefore: bmc.Status.PowerState,
		StartTime:        &now,
	}
	if err := r.Client.Status().Update(ctx, a); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // claimed by the server or another reconcile
		}
		return ctrl.Result{}, err
	}

	// Forced actions take the host, and this agent, down immediately, so the outcome is
	// recorded before the command is sent and corrected if the command fails.
	recordEvent(ctx, r.Client, a, r.Node, nil, clock(r.Now))
	a.Status.Phase = bmcv1.PhaseSucceeded
	a.Status.Message = fmt.Sprintf("%s sent to the local BMC through /dev/ipmi0", a.Spec.Action)
	done := clock(r.Now)
	a.Status.CompletionTime = &done
	if err := r.Client.Status().Update(ctx, a); err != nil {
		return ctrl.Result{}, fmt.Errorf("record action before execution: %w", err)
	}

	log.FromContext(ctx).Info("executing power action in-band", "action", a.Spec.Action, "requestedBy", a.Spec.RequestedBy)
	execCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	if _, err := r.Runner.Run(execCtx, "chassis", "power", verb); err != nil {
		recordEvent(ctx, r.Client, a, r.Node, err, clock(r.Now))
		failed := clock(r.Now)
		a.Status.Phase, a.Status.Message, a.Status.CompletionTime = bmcv1.PhaseFailed, err.Error(), &failed
		return ctrl.Result{}, r.Client.Status().Update(ctx, a)
	}
	return ctrl.Result{}, nil
}
