package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/agent"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/controller"
)

// maxActions bounds the number of actions returned by ListActions.
const maxActions = 100

// KubeOptions configures the Kubernetes backend.
type KubeOptions struct {
	// Namespace kube-bmc runs in; agent pods live here.
	Namespace     string
	AgentSelector labels.Selector
	AgentPort     int
	Credentials   controller.Credentials
	// StaleAfter applies to agents that do not renew a heartbeat Lease.
	StaleAfter time.Duration
}

// Kube is the Backend for a live cluster. Reads go through an informer cache.
type Kube struct {
	client client.Client
	opts   KubeOptions
	http   *http.Client
}

func NewKube(c client.Client, opts KubeOptions) *Kube {
	return &Kube{client: c, opts: opts, http: &http.Client{Timeout: 10 * time.Second}}
}

func (k *Kube) List(ctx context.Context) ([]View, error) {
	var bmcs bmcv1.BMCList
	if err := k.client.List(ctx, &bmcs); err != nil {
		return nil, err
	}
	var nodes corev1.NodeList
	if err := k.client.List(ctx, &nodes); err != nil {
		return nil, err
	}
	pods, err := k.agents(ctx)
	if err != nil {
		return nil, err
	}
	leases, err := k.leases(ctx)
	if err != nil {
		return nil, err
	}
	byNode := make(map[string]*corev1.Node, len(nodes.Items))
	for i := range nodes.Items {
		byNode[nodes.Items[i].Name] = &nodes.Items[i]
	}
	views := make([]View, 0, len(bmcs.Items))
	for i := range bmcs.Items {
		b := &bmcs.Items[i]
		views = append(views, k.view(ctx, b, byNode[b.Spec.NodeName], pods[b.Spec.NodeName], leases[b.Spec.NodeName]))
	}
	slices.SortFunc(views, func(a, b View) int { return strings.Compare(a.Name, b.Name) })
	return views, nil
}

func (k *Kube) Get(ctx context.Context, name string) (View, error) {
	b, err := k.bmc(ctx, name)
	if err != nil {
		return View{}, err
	}
	var node *corev1.Node
	n := &corev1.Node{}
	if err := k.client.Get(ctx, client.ObjectKey{Name: b.Spec.NodeName}, n); err == nil {
		node = n
	}
	pods, err := k.agents(ctx)
	if err != nil {
		return View{}, err
	}
	leases, err := k.leases(ctx)
	if err != nil {
		return View{}, err
	}
	return k.view(ctx, b, node, pods[b.Spec.NodeName], leases[b.Spec.NodeName]), nil
}

func (k *Kube) Live(ctx context.Context, name string) (*collector.Snapshot, error) {
	b, err := k.bmc(ctx, name)
	if err != nil {
		return nil, err
	}
	pods, err := k.agents(ctx)
	if err != nil {
		return nil, err
	}
	pod := pods[b.Spec.NodeName]
	if pod == nil || pod.Status.PodIP == "" {
		return nil, fmt.Errorf("no running agent on node %s", b.Spec.NodeName)
	}
	url := "http://" + net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(k.opts.AgentPort)) + "/api/v1/snapshot"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent %s: %w", pod.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent %s: HTTP %d", pod.Name, resp.StatusCode)
	}
	var snap collector.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, fmt.Errorf("agent %s: %w", pod.Name, err)
	}
	return &snap, nil
}

func (k *Kube) CreateAction(ctx context.Context, req ActionRequest) (*bmcv1.BMCAction, error) {
	if _, err := k.bmc(ctx, req.BMC); err != nil {
		return nil, err
	}
	a := &bmcv1.BMCAction{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.BMC + "-" + strings.ToLower(string(req.Action)) + "-",
			Labels:       map[string]string{"bmc.kube-bmc.io/bmc": req.BMC},
		},
		Spec: bmcv1.BMCActionSpec{BMCName: req.BMC, Action: req.Action, RequestedBy: req.RequestedBy, Reason: req.Reason},
	}
	if err := k.client.Create(ctx, a); err != nil {
		return nil, fmt.Errorf("create BMCAction: %w", err)
	}
	return a, nil
}

func (k *Kube) ListActions(ctx context.Context, bmc string) ([]bmcv1.BMCAction, error) {
	var list bmcv1.BMCActionList
	if err := k.client.List(ctx, &list); err != nil {
		return nil, err
	}
	items := slices.DeleteFunc(list.Items, func(a bmcv1.BMCAction) bool { return bmc != "" && a.Spec.BMCName != bmc })
	slices.SortFunc(items, func(a, b bmcv1.BMCAction) int {
		return b.CreationTimestamp.Compare(a.CreationTimestamp.Time)
	})
	if len(items) > maxActions {
		items = items[:maxActions]
	}
	return items, nil
}

func (k *Kube) GetAction(ctx context.Context, name string) (*bmcv1.BMCAction, error) {
	a := &bmcv1.BMCAction{}
	if err := k.client.Get(ctx, client.ObjectKey{Name: name}, a); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("bmcaction %s: %w", name, ErrNotFound)
		}
		return nil, err
	}
	return a, nil
}

func (k *Kube) bmc(ctx context.Context, name string) (*bmcv1.BMC, error) {
	b := &bmcv1.BMC{}
	if err := k.client.Get(ctx, client.ObjectKey{Name: name}, b); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("bmc %s: %w", name, ErrNotFound)
		}
		return nil, err
	}
	return b, nil
}

// agents returns the running agent pod for each node.
func (k *Kube) agents(ctx context.Context) (map[string]*corev1.Pod, error) {
	var pods corev1.PodList
	if err := k.client.List(ctx, &pods, client.InNamespace(k.opts.Namespace),
		client.MatchingLabelsSelector{Selector: k.opts.AgentSelector}); err != nil {
		return nil, err
	}
	res := map[string]*corev1.Pod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
			continue
		}
		res[p.Spec.NodeName] = p
	}
	return res, nil
}

// leases returns the agent heartbeat Lease for each node.
func (k *Kube) leases(ctx context.Context) (map[string]*coordinationv1.Lease, error) {
	var list coordinationv1.LeaseList
	if err := k.client.List(ctx, &list, client.InNamespace(k.opts.Namespace), client.HasLabels{agent.LeaseLabel}); err != nil {
		return nil, err
	}
	res := make(map[string]*coordinationv1.Lease, len(list.Items))
	for i := range list.Items {
		res[list.Items[i].Name] = &list.Items[i]
	}
	return res, nil
}

func (k *Kube) view(ctx context.Context, b *bmcv1.BMC, node *corev1.Node, pod *corev1.Pod, lease *coordinationv1.Lease) View {
	v := View{Name: b.Name, Created: b.CreationTimestamp, Spec: b.Spec, Status: b.Status}
	switch {
	case lease != nil:
		v.Stale = agent.LeaseExpired(lease, time.Now())
		if lease.Spec.RenewTime != nil {
			v.LastSeen = &metav1.Time{Time: lease.Spec.RenewTime.Time}
		}
	case b.Status.LastUpdated != nil:
		v.LastSeen = b.Status.LastUpdated
		v.Stale = time.Since(b.Status.LastUpdated.Time) > k.opts.StaleAfter
	default:
		v.Stale = true
	}
	if node != nil {
		v.Node = nodeInfo(node)
	}
	if pod != nil {
		v.Agent = &AgentInfo{Pod: pod.Name, IP: pod.Status.PodIP, Ready: podReady(pod)}
	}
	_, err := k.opts.Credentials.For(ctx, b)
	v.OOBConfigured = err == nil
	return v
}

func nodeInfo(n *corev1.Node) *NodeInfo {
	info := &NodeInfo{
		Unschedulable:  n.Spec.Unschedulable,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		OSImage:        n.Status.NodeInfo.OSImage,
		KernelVersion:  n.Status.NodeInfo.KernelVersion,
		CPU:            n.Status.Capacity.Cpu().String(),
		Memory:         gibibytes(n.Status.Capacity.Memory().Value()),
		GPUModel:       n.Labels["nvidia.com/gpu.product"],
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			info.Ready = c.Status == corev1.ConditionTrue
		}
	}
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			info.InternalIP = a.Address
			break
		}
	}
	for l := range n.Labels {
		if role, ok := strings.CutPrefix(l, "node-role.kubernetes.io/"); ok {
			info.Roles = append(info.Roles, role)
		}
	}
	slices.Sort(info.Roles)
	for res, q := range n.Status.Capacity {
		if strings.HasSuffix(string(res), "/gpu") {
			info.GPUs += q.Value()
		}
	}
	return info
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func gibibytes(b int64) string {
	const gi = 1 << 30
	return fmt.Sprintf("%d Gi", (b+gi/2)/gi)
}
