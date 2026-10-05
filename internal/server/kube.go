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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/oob"
)

type KubeOptions struct {
	// Namespace kube-bmc runs in; agent pods and credential Secrets live here.
	Namespace     string
	AgentSelector labels.Selector
	AgentPort     int
	// DefaultCredentials is the Secret used when a BMC has no spec.credentialsRef.
	DefaultCredentials string
	StaleAfter         time.Duration
}

// Kube reads BMCs, Nodes and agent Pods through an informer-backed client.
type Kube struct {
	cache  client.Reader // informer cache
	direct client.Client // uncached, for Secrets and writes
	opts   KubeOptions
	http   *http.Client
}

func NewKube(cache client.Reader, direct client.Client, opts KubeOptions) *Kube {
	return &Kube{cache: cache, direct: direct, opts: opts, http: &http.Client{Timeout: 10 * time.Second}}
}

func (k *Kube) List(ctx context.Context) ([]View, error) {
	var bmcs bmcv1.BMCList
	if err := k.cache.List(ctx, &bmcs); err != nil {
		return nil, err
	}
	var nodes corev1.NodeList
	if err := k.cache.List(ctx, &nodes); err != nil {
		return nil, err
	}
	pods, err := k.agents(ctx)
	if err != nil {
		return nil, err
	}
	byNode := map[string]*corev1.Node{}
	for i := range nodes.Items {
		byNode[nodes.Items[i].Name] = &nodes.Items[i]
	}
	views := make([]View, 0, len(bmcs.Items))
	for i := range bmcs.Items {
		b := &bmcs.Items[i]
		views = append(views, k.view(ctx, b, byNode[b.Spec.NodeName], pods[b.Spec.NodeName]))
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
	if err := k.cache.Get(ctx, client.ObjectKey{Name: b.Spec.NodeName}, n); err == nil {
		node = n
	}
	pods, err := k.agents(ctx)
	if err != nil {
		return View{}, err
	}
	return k.view(ctx, b, node, pods[b.Spec.NodeName]), nil
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

func (k *Kube) PowerState(ctx context.Context, name string) (bmcv1.PowerState, error) {
	t, _, err := k.target(ctx, name)
	if err != nil {
		return bmcv1.PowerUnknown, err
	}
	return oob.PowerState(ctx, t)
}

func (k *Kube) Power(ctx context.Context, name string, a oob.Action, actor string) error {
	t, b, err := k.target(ctx, name)
	if err != nil {
		return err
	}
	err = oob.Power(ctx, t, a)
	k.event(ctx, b, a, actor, err)
	return err
}

func (k *Kube) bmc(ctx context.Context, name string) (*bmcv1.BMC, error) {
	b := &bmcv1.BMC{}
	if err := k.cache.Get(ctx, client.ObjectKey{Name: name}, b); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("bmc %s: %w", name, ErrNotFound)
		}
		return nil, err
	}
	return b, nil
}

// agents returns the agent pod per node name.
func (k *Kube) agents(ctx context.Context) (map[string]*corev1.Pod, error) {
	var pods corev1.PodList
	if err := k.cache.List(ctx, &pods, client.InNamespace(k.opts.Namespace),
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

func (k *Kube) view(ctx context.Context, b *bmcv1.BMC, node *corev1.Node, pod *corev1.Pod) View {
	v := View{Name: b.Name, Created: b.CreationTimestamp, Spec: b.Spec, Status: b.Status}
	if lu := b.Status.LastUpdated; lu == nil || time.Since(lu.Time) > k.opts.StaleAfter {
		v.Stale = true
	}
	if node != nil {
		v.Node = nodeInfo(node)
	}
	if pod != nil {
		v.Agent = &AgentInfo{Pod: pod.Name, IP: pod.Status.PodIP, Ready: podReady(pod)}
	}
	_, err := k.credentials(ctx, b)
	v.OOBConfigured = err == nil
	return v
}

func (k *Kube) target(ctx context.Context, name string) (oob.Target, *bmcv1.BMC, error) {
	b, err := k.bmc(ctx, name)
	if err != nil {
		return oob.Target{}, nil, err
	}
	creds, err := k.credentials(ctx, b)
	if err != nil {
		return oob.Target{}, nil, err
	}
	t, err := oob.TargetFor(b, creds)
	return t, b, err
}

// credentials reads the BMC's Secret. Secrets must live in the kube-bmc namespace: the server is
// deliberately not granted cluster-wide Secret access.
func (k *Kube) credentials(ctx context.Context, b *bmcv1.BMC) (oob.Credentials, error) {
	name := k.opts.DefaultCredentials
	if ref := b.Spec.CredentialsRef; ref != nil && ref.Name != "" {
		name = ref.Name
	}
	if name == "" {
		return oob.Credentials{}, fmt.Errorf("no credentials configured for %s", b.Name)
	}
	s := &corev1.Secret{}
	if err := k.cache.Get(ctx, client.ObjectKey{Namespace: k.opts.Namespace, Name: name}, s); err != nil {
		return oob.Credentials{}, fmt.Errorf("credentials secret %s/%s: %w", k.opts.Namespace, name, err)
	}
	c := oob.Credentials{Username: string(s.Data["username"]), Password: string(s.Data["password"])}
	if c.Username == "" {
		return c, fmt.Errorf("secret %s/%s has no username key", k.opts.Namespace, name)
	}
	return c, nil
}

// event records every power action on the Node, so it shows up in `kubectl describe node`.
func (k *Kube) event(ctx context.Context, b *bmcv1.BMC, a oob.Action, actor string, actionErr error) {
	typ, reason, msg := corev1.EventTypeNormal, "BMCPowerAction", fmt.Sprintf("%s requested %s via kube-bmc", actor, a)
	if actionErr != nil {
		typ, reason, msg = corev1.EventTypeWarning, "BMCPowerActionFailed", msg+": "+actionErr.Error()
	}
	now := metav1.Now()
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: b.Spec.NodeName + ".", Namespace: metav1.NamespaceDefault},
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: b.Spec.NodeName, APIVersion: "v1"},
		Reason:         reason, Message: msg, Type: typ,
		Source:         corev1.EventSource{Component: "kube-bmc"},
		FirstTimestamp: now, LastTimestamp: now, Count: 1,
	}
	_ = k.direct.Create(ctx, ev)
}

func nodeInfo(n *corev1.Node) *NodeInfo {
	info := &NodeInfo{
		Unschedulable:  n.Spec.Unschedulable,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		OSImage:        n.Status.NodeInfo.OSImage,
		KernelVersion:  n.Status.NodeInfo.KernelVersion,
		CPU:            n.Status.Capacity.Cpu().String(),
		Memory:         humanBytes(n.Status.Capacity.Memory().Value()),
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

func humanBytes(b int64) string {
	const gi = 1 << 30
	return fmt.Sprintf("%d Gi", (b+gi/2)/gi)
}
