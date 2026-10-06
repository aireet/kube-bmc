package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

// powerVerbs maps the power actions a node agent executes through the local BMC interface
// to `ipmitool chassis power` sub-commands. On cannot be executed in-band because the agent
// does not run while the host is off; GracefulRestart has no IPMI equivalent.
var powerVerbs = map[bmcv1.ActionType]string{
	bmcv1.ActionGracefulShutdown: "soft",
	bmcv1.ActionForceOff:         "off",
	bmcv1.ActionForceRestart:     "reset",
	bmcv1.ActionPowerCycle:       "cycle",
}

// InBand reports whether a node agent executes a through the local BMC interface.
func InBand(a bmcv1.ActionType) bool {
	_, power := powerVerbs[a]
	return power || a == bmcv1.ActionIdentifyOn || a == bmcv1.ActionIdentifyOff || a == bmcv1.ActionClearSEL
}

// SELArchiveLabel marks ConfigMaps that hold System Event Logs saved by ClearSEL. The
// log is stored gzip-compressed under SELArchiveKey.
const (
	SELArchiveLabel = "bmc.kube-bmc.io/sel-archive"
	SELArchiveKey   = "sel.txt.gz"
	// BMCLabel names the BMC an object belongs to.
	BMCLabel = "bmc.kube-bmc.io/bmc"
)

// InBandReconciler runs in the node agent and executes actions for its node through
// /dev/ipmi0. No BMC credentials or network access to the BMC are needed.
type InBandReconciler struct {
	Client client.Client
	Node   string
	IPMI   *ipmi.Client
	// Namespace receives the SEL archives created by ClearSEL.
	Namespace string
	// SELArchives is the number of SEL archives kept per server; older ones are deleted.
	SELArchives int
	// OnSELCleared is called after the System Event Log was cleared.
	OnSELCleared func()
	// Enabled must also be set on the server; when false, the agent leaves actions alone
	// and the server rejects them.
	Enabled bool
	// Timeout bounds the execution of an action and sets its deadline.
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
	if !InBand(a.Spec.Action) || a.Spec.BMCName != r.Node || a.Status.Phase != "" {
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
		Deadline:         &metav1.Time{Time: now.Add(r.Timeout)},
	}
	if err := r.Client.Status().Update(ctx, a); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // claimed by the server or another reconcile
		}
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("executing action in-band", "action", a.Spec.Action, "requestedBy", a.Spec.RequestedBy)

	execCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	if verb, ok := powerVerbs[a.Spec.Action]; ok {
		return ctrl.Result{}, r.power(ctx, execCtx, a, verb)
	}

	var msg string
	var err error
	switch a.Spec.Action {
	case bmcv1.ActionIdentifyOn:
		msg = "identify light turned on until IdentifyOff"
		if note, e := r.IPMI.Identify(execCtx, true); e != nil {
			err = e
		} else if note != "" {
			msg = "identify light turned on; " + note
		}
	case bmcv1.ActionIdentifyOff:
		msg = "identify light turned off"
		_, err = r.IPMI.Identify(execCtx, false)
	case bmcv1.ActionClearSEL:
		msg, err = r.clearSEL(execCtx, a)
	}
	recordEvent(ctx, r.Client, a, r.Node, err, clock(r.Now))
	done := clock(r.Now)
	a.Status.CompletionTime = &done
	if err != nil {
		a.Status.Phase, a.Status.Message = bmcv1.PhaseFailed, err.Error()
	} else {
		a.Status.Phase, a.Status.Message = bmcv1.PhaseSucceeded, msg
	}
	return ctrl.Result{}, r.Client.Status().Update(ctx, a)
}

// power executes a power action. Forced actions take the host, and this agent, down
// immediately, so the outcome is recorded before the command is sent and corrected if the
// command fails.
func (r *InBandReconciler) power(ctx, execCtx context.Context, a *bmcv1.BMCAction, verb string) error {
	recordEvent(ctx, r.Client, a, r.Node, nil, clock(r.Now))
	done := clock(r.Now)
	a.Status.Phase, a.Status.CompletionTime = bmcv1.PhaseSucceeded, &done
	a.Status.Message = fmt.Sprintf("%s sent to the local BMC through /dev/ipmi0", a.Spec.Action)
	if err := r.Client.Status().Update(ctx, a); err != nil {
		return fmt.Errorf("record action before execution: %w", err)
	}
	if err := r.IPMI.ChassisPower(execCtx, verb); err != nil {
		recordEvent(ctx, r.Client, a, r.Node, err, clock(r.Now))
		failed := clock(r.Now)
		a.Status.Phase, a.Status.Message, a.Status.CompletionTime = bmcv1.PhaseFailed, err.Error(), &failed
		return r.Client.Status().Update(ctx, a)
	}
	return nil
}

// clearSEL saves the complete log to a compressed ConfigMap and clears the log only after
// the archive was stored. Archives are kept per server up to SELArchives, independently of
// the action TTL.
func (r *InBandReconciler) clearSEL(ctx context.Context, a *bmcv1.BMCAction) (string, error) {
	text, err := r.IPMI.SELText(ctx)
	if err != nil {
		return "", fmt.Errorf("read the System Event Log; it was not cleared: %w", err)
	}
	entries := strings.Count(strings.TrimSpace(string(text)), "\n") + 1
	if len(bytes.TrimSpace(text)) == 0 {
		entries = 0
	}

	ts := clock(r.Now).UTC()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("sel-%s-%s", r.Node, ts.Format("20060102-150405")),
			Namespace: r.Namespace,
			Labels:    map[string]string{SELArchiveLabel: "true", BMCLabel: r.Node},
			Annotations: map[string]string{
				"bmc.kube-bmc.io/bmcaction":    a.Name,
				"bmc.kube-bmc.io/requested-by": a.Spec.RequestedBy,
				"bmc.kube-bmc.io/reason":       a.Spec.Reason,
				"bmc.kube-bmc.io/entries":      fmt.Sprint(entries),
			},
		},
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(text)
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("compress the System Event Log; it was not cleared: %w", err)
	}
	cm.BinaryData = map[string][]byte{SELArchiveKey: buf.Bytes()}
	if err := r.Client.Create(ctx, cm); err != nil {
		return "", fmt.Errorf("save the System Event Log; it was not cleared: %w", err)
	}
	a.Status.SELArchive = r.Namespace + "/" + cm.Name
	if err := r.pruneSELArchives(ctx); err != nil {
		log.FromContext(ctx).Error(err, "deleting old SEL archives failed")
	}

	if err := r.IPMI.ClearSEL(ctx); err != nil {
		return "", fmt.Errorf("clear the System Event Log (archive kept in %s): %w", a.Status.SELArchive, err)
	}
	if r.OnSELCleared != nil {
		r.OnSELCleared()
	}
	return fmt.Sprintf("System Event Log cleared; %d entries saved to ConfigMap %s", entries, a.Status.SELArchive), nil
}

// pruneSELArchives deletes the oldest archives of this server beyond SELArchives. Archive
// names end with a UTC timestamp, so they sort chronologically.
func (r *InBandReconciler) pruneSELArchives(ctx context.Context) error {
	var list corev1.ConfigMapList
	if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace),
		client.MatchingLabels{SELArchiveLabel: "true", BMCLabel: r.Node}); err != nil {
		return err
	}
	keep := max(r.SELArchives, 1)
	if len(list.Items) <= keep {
		return nil
	}
	slices.SortFunc(list.Items, func(a, b corev1.ConfigMap) int { return strings.Compare(a.Name, b.Name) })
	var errs []error
	for i := range list.Items[:len(list.Items)-keep] {
		if err := r.Client.Delete(ctx, &list.Items[i]); client.IgnoreNotFound(err) != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
