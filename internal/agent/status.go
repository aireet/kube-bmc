package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
)

// Deadbands for readings. A reading change within its deadband is written only on the
// next refresh.
const (
	wattsDeadbandRatio = 0.20
	wattsDeadbandMin   = 100
	inletDeadband      = 3
	selDeadband        = 5 // percentage points
	// problemClearDelay is how long a problem must be absent before it is removed. New
	// problems are reported immediately; the delay suppresses flapping around thresholds.
	problemClearDelay = 5 * time.Minute
)

// statusWriter writes the BMC status. The agent is the only writer of the status, so it
// keeps the last written object and patches against it without reading it first.
type statusWriter struct {
	k8s     client.Client
	node    string
	version string
	refresh time.Duration
	log     *slog.Logger

	obj       *bmcv1.BMC
	lastWrite time.Time
	// problemSeen records when each problem (by severity and source) was last observed.
	problemSeen map[problemKey]seenProblem
}

type problemKey struct {
	severity bmcv1.Health
	source   string
}

type seenProblem struct {
	problem bmcv1.Problem
	at      time.Time
}

func (w *statusWriter) write(ctx context.Context, snap *collector.Snapshot, node func(context.Context) (*corev1.Node, error)) error {
	if w.obj == nil {
		obj, err := w.ensure(ctx, node)
		if err != nil {
			return err
		}
		w.obj = obj
	}

	now := time.Now()
	next := collector.Status(snap)
	next.Problems, next.Health = w.debounce(next.Problems, next.Health, now)
	next.Conditions = slices.Clone(w.obj.Status.Conditions)
	meta.SetStatusCondition(&next.Conditions, readyCondition(snap.Errors, w.obj.Generation))

	reason := writeReason(w.obj.Status, next, w.lastWrite, now, w.refresh)
	if reason == "" {
		return nil
	}
	next.AgentVersion = w.version
	next.LastUpdated = ptr.To(metav1.Now())

	desired := w.obj.DeepCopy()
	desired.Status = next
	if err := w.k8s.Status().Patch(ctx, desired, client.MergeFrom(w.obj)); err != nil {
		if apierrors.IsNotFound(err) {
			w.obj = nil // deleted; recreate on the next round
		}
		return fmt.Errorf("patch status: %w", err)
	}
	w.obj, w.lastWrite = desired, time.Now()
	statusWrites.WithLabelValues(reason).Inc()
	w.log.Debug("status written", "reason", reason)
	return nil
}

// debounce keeps problems that disappeared less than problemClearDelay ago and raises
// health accordingly.
func (w *statusWriter) debounce(current []bmcv1.Problem, health bmcv1.Health, now time.Time) ([]bmcv1.Problem, bmcv1.Health) {
	if w.problemSeen == nil {
		w.problemSeen = map[problemKey]seenProblem{}
	}
	for _, p := range current {
		w.problemSeen[problemKey{p.Severity, p.Source}] = seenProblem{problem: p, at: now}
	}
	out := make([]bmcv1.Problem, 0, len(w.problemSeen))
	for k, s := range w.problemSeen {
		if now.Sub(s.at) > problemClearDelay {
			delete(w.problemSeen, k)
			continue
		}
		out = append(out, s.problem)
		if s.problem.Severity.Rank() > health.Rank() {
			health = s.problem.Severity
		}
	}
	slices.SortFunc(out, func(a, b bmcv1.Problem) int {
		if d := b.Severity.Rank() - a.Severity.Rank(); d != 0 {
			return d
		}
		return strings.Compare(a.Source, b.Source)
	})
	if len(out) == 0 {
		out = nil
	}
	return out, health
}

// writeReason returns why next must be written, or "" if the write can be skipped.
func writeReason(prev, next bmcv1.BMCStatus, lastWrite, now time.Time, refresh time.Duration) string {
	switch {
	case lastWrite.IsZero():
		return "initial"
	case !equality.Semantic.DeepEqual(stable(prev), stable(next)):
		return "change"
	case readingsDrifted(prev, next):
		return "readings"
	case now.Sub(lastWrite) >= refresh:
		return "refresh"
	}
	return ""
}

// stable returns the parts of a status whose changes are written immediately.
func stable(s bmcv1.BMCStatus) bmcv1.BMCStatus {
	s.PowerWatts, s.InletTemperature, s.LastUpdated, s.AgentVersion = nil, nil, nil, ""
	s.SEL.Entries, s.SEL.LastAddTime = 0, ""
	s.Sensors = bmcv1.SensorSummary{} // statistics; refreshed with the readings
	s.SEL.UsedPercent = s.SEL.UsedPercent / selDeadband * selDeadband
	s.Problems = slices.Clone(s.Problems)
	for i := range s.Problems {
		s.Problems[i].Message = "" // messages contain live readings
	}
	s.Conditions = slices.Clone(s.Conditions)
	for i := range s.Conditions {
		c := &s.Conditions[i]
		c.Message, c.LastTransitionTime = "", metav1.Time{}
	}
	return s
}

func readingsDrifted(prev, next bmcv1.BMCStatus) bool {
	if changedBeyond(prev.PowerWatts, next.PowerWatts, func(p float64) float64 {
		return math.Max(wattsDeadbandMin, p*wattsDeadbandRatio)
	}) {
		return true
	}
	if changedBeyond(prev.InletTemperature, next.InletTemperature, func(float64) float64 { return inletDeadband }) {
		return true
	}
	return abs(prev.SEL.UsedPercent-next.SEL.UsedPercent) >= selDeadband
}

func changedBeyond(prev, next *int32, band func(prev float64) float64) bool {
	if (prev == nil) != (next == nil) {
		return true
	}
	if prev == nil {
		return false
	}
	return math.Abs(float64(*next-*prev)) >= band(float64(*prev))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func readyCondition(errs map[string]string, gen int64) metav1.Condition {
	c := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Collecting",
		Message: "In-band IPMI collection is working", ObservedGeneration: gen}
	if len(errs) == 0 {
		return c
	}
	phases := make([]string, 0, len(errs))
	for p, e := range errs {
		phases = append(phases, p+": "+e)
	}
	slices.Sort(phases)
	c.Message = strings.Join(phases, "; ")
	c.Reason = "PartialCollection"
	if _, ok := errs["sensors"]; ok {
		c.Status, c.Reason = metav1.ConditionFalse, "CollectionFailed"
	}
	return c
}

// ensure returns the BMC object of the node, creating it if needed.
func (w *statusWriter) ensure(ctx context.Context, node func(context.Context) (*corev1.Node, error)) (*bmcv1.BMC, error) {
	obj := &bmcv1.BMC{}
	err := w.k8s.Get(ctx, client.ObjectKey{Name: w.node}, obj)
	if err == nil || !apierrors.IsNotFound(err) {
		return obj, err
	}
	n, err := node(ctx)
	if err != nil {
		return nil, err
	}
	obj = &bmcv1.BMC{
		ObjectMeta: metav1.ObjectMeta{
			Name:   w.node,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "kube-bmc"},
			// The BMC object is garbage-collected together with its Node.
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: n.Name, UID: n.UID}},
		},
		Spec: bmcv1.BMCSpec{NodeName: w.node, Protocol: bmcv1.ProtocolRedfish, InsecureSkipVerify: true},
	}
	if err := w.k8s.Create(ctx, obj); err != nil {
		return nil, fmt.Errorf("create bmc: %w", err)
	}
	w.log.Info("registered BMC")
	return obj, nil
}
