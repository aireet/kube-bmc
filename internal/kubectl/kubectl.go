// Package kubectl implements the kubectl-bmc plugin.
//
// The plugin talks only to the Kubernetes API server with the caller's kubeconfig:
// BMC and BMCAction objects are read and created directly, and live sensor and event
// data is fetched from the node agents through the API server's pod proxy. Access is
// therefore governed entirely by Kubernetes RBAC.
package kubectl

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/agent"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/controller"
	"github.com/aireet/kube-bmc/ipmi"
)

// Clients is what the commands need from the cluster.
type Clients struct {
	Client client.Client
	// Snapshot returns the live data of a node's agent.
	Snapshot func(ctx context.Context, node string) (*collector.Snapshot, error)
	// Whoami returns the authenticated Kubernetes username.
	Whoami func(ctx context.Context) (string, error)
	// LastSeen returns the last agent heartbeat per node. It may be nil.
	LastSeen func(ctx context.Context) (map[string]time.Time, error)
}

type options struct {
	streams   genericiooptions.IOStreams
	config    *genericclioptions.ConfigFlags
	namespace string
	color     bool
	clients   func() (*Clients, error)
}

// NewCommand returns the root command. clients may be nil, in which case they are
// built from the kubeconfig flags.
func NewCommand(streams genericiooptions.IOStreams, version string, clients *Clients) *cobra.Command {
	o := &options{streams: streams, config: genericclioptions.NewConfigFlags(true)}
	o.clients = func() (*Clients, error) {
		if clients != nil {
			return clients, nil
		}
		return o.kubeClients()
	}

	root := &cobra.Command{
		Use:           "kubectl-bmc",
		Short:         "Inspect and control server BMCs managed by kube-bmc",
		SilenceUsage:  true,
		SilenceErrors: true,
		Example: `  kubectl bmc list
  kubectl bmc describe gpu-01
  kubectl bmc sensors gpu-01 --problems
  kubectl bmc events gpu-01 --limit 20
  kubectl bmc power gpu-01 ForceRestart --reason "kernel hang" --wait
  kubectl bmc locate gpu-01
  kubectl bmc clear-sel gpu-01 --reason "log full"
  kubectl bmc actions`,
		PersistentPreRun: func(*cobra.Command, []string) {
			o.color = o.color && isTerminal(streams.Out)
		},
	}
	o.config.AddFlags(root.PersistentFlags())
	root.PersistentFlags().StringVar(&o.namespace, "kube-bmc-namespace", "kube-bmc-system", "namespace where kube-bmc is installed")
	root.PersistentFlags().BoolVar(&o.color, "color", true, "colorize output when writing to a terminal")

	root.AddCommand(o.listCmd(), o.describeCmd(), o.sensorsCmd(), o.eventsCmd(), o.powerCmd(), o.locateCmd(), o.clearSELCmd(),
		o.selArchivesCmd(), o.actionsCmd(),
		&cobra.Command{
			Use:   "version",
			Short: "Print the plugin version",
			Run:   func(*cobra.Command, []string) { fmt.Fprintln(streams.Out, version) },
		})
	return root
}

func (o *options) kubeClients() (*Clients, error) {
	cfg, err := o.config.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = bmcv1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Clients{
		Client: c,
		Snapshot: func(ctx context.Context, node string) (*collector.Snapshot, error) {
			return agentSnapshot(ctx, cs, o.namespace, node)
		},
		LastSeen: func(ctx context.Context) (map[string]time.Time, error) {
			leases, err := cs.CoordinationV1().Leases(o.namespace).List(ctx, metav1.ListOptions{LabelSelector: agent.LeaseLabel})
			if err != nil {
				return nil, err
			}
			seen := map[string]time.Time{}
			for _, l := range leases.Items {
				if l.Spec.RenewTime != nil {
					seen[l.Name] = l.Spec.RenewTime.Time
				}
			}
			return seen, nil
		},
		Whoami: func(ctx context.Context) (string, error) {
			r, err := cs.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
			if err != nil {
				return "", fmt.Errorf("determine your username: %w", err)
			}
			return r.Status.UserInfo.Username, nil
		},
	}, nil
}

// agentSnapshot reads /api/v1/snapshot of the agent on node through the pod proxy.
func agentSnapshot(ctx context.Context, cs kubernetes.Interface, ns, node string) (*collector.Snapshot, error) {
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=kube-bmc,app.kubernetes.io/component=agent",
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil {
		return nil, fmt.Errorf("find agent on %s: %w", node, err)
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			pod = &pods.Items[i]
		}
	}
	if pod == nil {
		return nil, fmt.Errorf("no running kube-bmc agent on node %s in namespace %s", node, ns)
	}
	port := "9580"
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			if p.Name == "http" {
				port = fmt.Sprint(p.ContainerPort)
			}
		}
	}
	raw, err := cs.CoreV1().Pods(ns).ProxyGet("http", pod.Name, port, "/api/v1/snapshot", nil).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent %s: %w", pod.Name, err)
	}
	var snap collector.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("agent %s: %w", pod.Name, err)
	}
	return &snap, nil
}

func (o *options) listCmd() *cobra.Command {
	var output, health string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "get"},
		Short:   "List BMCs with health, power and inventory",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.clients()
			if err != nil {
				return err
			}
			var list bmcv1.BMCList
			if err := c.Client.List(cmd.Context(), &list); err != nil {
				return err
			}
			items := slices.DeleteFunc(list.Items, func(b bmcv1.BMC) bool {
				return health != "" && !strings.EqualFold(string(b.Status.Health), health)
			})
			slices.SortFunc(items, func(a, b bmcv1.BMC) int { return strings.Compare(a.Name, b.Name) })
			switch output {
			case "json", "yaml":
				return o.encode(output, items)
			case "name":
				for _, b := range items {
					fmt.Fprintln(o.streams.Out, "bmc/"+b.Name)
				}
				return nil
			case "", "wide":
				var seen map[string]time.Time
				if output == "wide" && c.LastSeen != nil {
					seen, _ = c.LastSeen(cmd.Context()) // optional; requires list on leases
				}
				printBMCs(o.streams.Out, items, seen, output == "wide", o.color)
				return nil
			}
			return fmt.Errorf("unsupported output format %q", output)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: wide, json, yaml or name")
	cmd.Flags().StringVar(&health, "health", "", "only BMCs with this health (OK, Warning, Critical, Unknown)")
	return cmd
}

func (o *options) describeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe NAME",
		Short: "Show inventory, health, problems and recent actions of a BMC",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.clients()
			if err != nil {
				return err
			}
			b := &bmcv1.BMC{}
			if err := c.Client.Get(cmd.Context(), client.ObjectKey{Name: args[0]}, b); err != nil {
				return err
			}
			actions, err := actionsFor(cmd.Context(), c.Client, b.Name)
			if err != nil {
				return err
			}
			describe(o.streams.Out, b, actions, o.color)
			return nil
		},
	}
}

func (o *options) sensorsCmd() *cobra.Command {
	var typ string
	var problems, all bool
	cmd := &cobra.Command{
		Use:   "sensors NAME",
		Short: "Show live sensor readings and thresholds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snap, err := o.snapshot(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printSensors(o.streams.Out, snap.Sensors, sensorFilter{Type: ipmi.SensorType(typ), Problems: problems, All: all}, o.color)
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "only sensors of this type: temperature, fan, voltage, power, current, utilization, discrete")
	cmd.Flags().BoolVar(&problems, "problems", false, "only sensors in warning or critical state")
	cmd.Flags().BoolVar(&all, "all", false, "include sensors without a reading")
	return cmd
}

func (o *options) eventsCmd() *cobra.Command {
	var limit int
	var grep string
	cmd := &cobra.Command{
		Use:   "events NAME",
		Short: "Show the newest System Event Log entries",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snap, err := o.snapshot(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printEvents(o.streams.Out, snap, limit, grep)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 30, "maximum number of events")
	cmd.Flags().StringVar(&grep, "grep", "", "only events containing this text (case-insensitive)")
	return cmd
}

func (o *options) snapshot(ctx context.Context, name string) (*collector.Snapshot, error) {
	c, err := o.clients()
	if err != nil {
		return nil, err
	}
	b := &bmcv1.BMC{}
	if err := c.Client.Get(ctx, client.ObjectKey{Name: name}, b); err != nil {
		return nil, err
	}
	return c.Snapshot(ctx, b.Spec.NodeName)
}

func (o *options) powerCmd() *cobra.Command {
	var reason string
	var yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "power NAME ACTION",
		Short: "Request a power action: On, GracefulShutdown, GracefulRestart, ForceRestart, PowerCycle or ForceOff",
		Long: "Creates a BMCAction that the kube-bmc server executes out-of-band through the BMC.\n" +
			"Requires permission to create bmcactions.bmc.kube-bmc.io. The action interrupts every workload on the node.",
		Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 1 {
				var names []string
				for _, a := range bmcv1.PowerActions {
					names = append(names, string(a))
				}
				return names, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name, action := args[0], bmcv1.ActionType(args[1])
			if !action.IsPower() {
				return fmt.Errorf("unsupported power action %q; use one of %v", action, bmcv1.PowerActions)
			}
			if strings.TrimSpace(reason) == "" {
				return errors.New("--reason is required")
			}
			c, err := o.clients()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			b := &bmcv1.BMC{}
			if err := c.Client.Get(ctx, client.ObjectKey{Name: name}, b); err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(o.streams.Out, "%s will be sent to the BMC of %s (%s %s, power %s).\nEvery workload on the node is interrupted. ",
					action, name, b.Status.Device.Manufacturer, b.Status.Device.Product, orDash(string(b.Status.PowerState)))
				if err := o.confirm(name); err != nil {
					return err
				}
			}
			return o.request(ctx, c, name, action, reason, wait, timeout)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "justification recorded with the action (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the action finishes")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "how long --wait waits")
	return cmd
}

// confirm asks the user to type the server name.
func (o *options) confirm(name string) error {
	fmt.Fprint(o.streams.Out, "Type the server name to confirm: ")
	line, _ := bufio.NewReader(o.streams.In).ReadString('\n')
	if strings.TrimSpace(line) != name {
		return errors.New("confirmation did not match; nothing was done")
	}
	return nil
}

// request creates a BMCAction and optionally waits for it to finish.
func (o *options) request(ctx context.Context, c *Clients, name string, action bmcv1.ActionType, reason string, wait bool, timeout time.Duration) error {
	user, err := c.Whoami(ctx)
	if err != nil {
		return err
	}
	a := &bmcv1.BMCAction{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name + "-" + strings.ToLower(string(action)) + "-",
			Labels:       map[string]string{"bmc.kube-bmc.io/bmc": name},
		},
		Spec: bmcv1.BMCActionSpec{BMCName: name, Action: action, RequestedBy: user, Reason: strings.TrimSpace("[kubectl] " + reason)},
	}
	if err := c.Client.Create(ctx, a); err != nil {
		return fmt.Errorf("create BMCAction: %w", err)
	}
	fmt.Fprintf(o.streams.Out, "bmcaction/%s created\n", a.Name)
	if !wait {
		return nil
	}
	return o.waitAction(ctx, c.Client, a.Name, timeout)
}

func (o *options) locateCmd() *cobra.Command {
	var off bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "locate NAME",
		Short: "Turn the identify light on until it is turned off with --off",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.clients()
			if err != nil {
				return err
			}
			if err := c.Client.Get(cmd.Context(), client.ObjectKey{Name: args[0]}, &bmcv1.BMC{}); err != nil {
				return err
			}
			action := bmcv1.ActionIdentifyOn
			if off {
				action = bmcv1.ActionIdentifyOff
			}
			return o.request(cmd.Context(), c, args[0], action, "", true, timeout)
		},
	}
	cmd.Flags().BoolVar(&off, "off", false, "turn the identify light off")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "how long to wait for the result")
	return cmd
}

func (o *options) clearSELCmd() *cobra.Command {
	var reason string
	var yes bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "clear-sel NAME",
		Short: "Save the System Event Log to a ConfigMap and clear it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if strings.TrimSpace(reason) == "" {
				return errors.New("--reason is required")
			}
			c, err := o.clients()
			if err != nil {
				return err
			}
			b := &bmcv1.BMC{}
			if err := c.Client.Get(cmd.Context(), client.ObjectKey{Name: name}, b); err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(o.streams.Out, "The System Event Log of %s (%d entries, %d%% used) will be saved to a ConfigMap and cleared. ",
					name, b.Status.SEL.Entries, b.Status.SEL.UsedPercent)
				if err := o.confirm(name); err != nil {
					return err
				}
			}
			return o.request(cmd.Context(), c, name, bmcv1.ActionClearSEL, reason, true, timeout)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "justification recorded with the action (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "how long to wait for the result")
	return cmd
}

func (o *options) selArchivesCmd() *cobra.Command {
	var show string
	cmd := &cobra.Command{
		Use:   "sel-archives NAME",
		Short: "List the System Event Logs saved by clear-sel, or print one with --show",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.clients()
			if err != nil {
				return err
			}
			var list corev1.ConfigMapList
			if err := c.Client.List(cmd.Context(), &list, client.InNamespace(o.namespace),
				client.MatchingLabels{controller.SELArchiveLabel: "true", controller.BMCLabel: args[0]}); err != nil {
				return err
			}
			if show != "" {
				for _, cm := range list.Items {
					if cm.Name == show {
						return printArchive(o.streams.Out, &cm)
					}
				}
				return fmt.Errorf("no SEL archive %q for %s", show, args[0])
			}
			printArchives(o.streams.Out, list.Items)
			return nil
		},
	}
	cmd.Flags().StringVar(&show, "show", "", "print the log saved in this archive")
	return cmd
}

func printArchive(w io.Writer, cm *corev1.ConfigMap) error {
	if text, ok := cm.Data["sel.txt"]; ok { // uncompressed archives created by v0.3.0
		_, err := io.WriteString(w, text)
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(cm.BinaryData[controller.SELArchiveKey]))
	if err != nil {
		return fmt.Errorf("archive %s: %w", cm.Name, err)
	}
	_, err = io.Copy(w, zr)
	return err
}

func (o *options) waitAction(ctx context.Context, c client.Client, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		a := &bmcv1.BMCAction{}
		if err := c.Get(ctx, client.ObjectKey{Name: name}, a); err != nil {
			return err
		}
		if a.Status.Phase.Done() {
			fmt.Fprintf(o.streams.Out, "%s: %s\n", a.Status.Phase, a.Status.Message)
			if a.Status.Phase != bmcv1.PhaseSucceeded {
				return fmt.Errorf("action %s", strings.ToLower(string(a.Status.Phase)))
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for bmcaction/%s (phase %s)", name, orDash(string(a.Status.Phase)))
		case <-tick.C:
		}
	}
}

func (o *options) actionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "actions [NAME]",
		Short: "List power actions, newest first",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.clients()
			if err != nil {
				return err
			}
			bmc := ""
			if len(args) == 1 {
				bmc = args[0]
			}
			actions, err := actionsFor(cmd.Context(), c.Client, bmc)
			if err != nil {
				return err
			}
			printActions(o.streams.Out, actions, o.color)
			return nil
		},
	}
}

func actionsFor(ctx context.Context, c client.Client, bmc string) ([]bmcv1.BMCAction, error) {
	var list bmcv1.BMCActionList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	items := slices.DeleteFunc(list.Items, func(a bmcv1.BMCAction) bool { return bmc != "" && a.Spec.BMCName != bmc })
	slices.SortFunc(items, func(a, b bmcv1.BMCAction) int { return b.CreationTimestamp.Compare(a.CreationTimestamp.Time) })
	return items, nil
}

func (o *options) encode(format string, v any) error {
	if format == "json" {
		enc := json.NewEncoder(o.streams.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = o.streams.Out.Write(b)
	return err
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
