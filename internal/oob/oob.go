// Package oob performs out-of-band power operations against a BMC over the network,
// which keeps working when the node (and therefore its agent) is down.
package oob

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

// Action is a power operation. The names mirror Redfish ResetType values.
type Action string

const (
	On               Action = "On"
	GracefulShutdown Action = "GracefulShutdown"
	ForceOff         Action = "ForceOff"
	GracefulRestart  Action = "GracefulRestart"
	ForceRestart     Action = "ForceRestart"
	PowerCycle       Action = "PowerCycle"
)

// Actions lists every supported action, in the order the UI shows them.
var Actions = []Action{On, GracefulShutdown, GracefulRestart, ForceRestart, PowerCycle, ForceOff}

func (a Action) Valid() bool {
	for _, x := range Actions {
		if a == x {
			return true
		}
	}
	return false
}

// ipmitool chassis power sub-commands for each action.
var ipmiVerb = map[Action]string{
	On: "on", GracefulShutdown: "soft", ForceOff: "off", ForceRestart: "reset", PowerCycle: "cycle",
	// IPMI has no graceful restart; a soft-off followed by on is not atomic, so it is not offered.
}

type Credentials struct{ Username, Password string }

// Target is everything needed to reach one BMC.
type Target struct {
	Address  string
	Protocol bmcv1.Protocol
	Insecure bool
	Creds    Credentials
}

// TargetFor resolves the address of a BMC: spec override first, then the in-band discovered IP.
func TargetFor(b *bmcv1.BMC, creds Credentials) (Target, error) {
	addr := b.Spec.Address
	if addr == "" {
		addr = b.Status.Network.IPAddress
	}
	if addr == "" {
		return Target{}, fmt.Errorf("BMC %s has no address: set spec.address or wait for the agent to discover it", b.Name)
	}
	proto := b.Spec.Protocol
	if proto == "" {
		proto = bmcv1.ProtocolRedfish
	}
	return Target{Address: addr, Protocol: proto, Insecure: b.Spec.InsecureSkipVerify, Creds: creds}, nil
}

// PowerState reads the current power state.
func PowerState(ctx context.Context, t Target) (bmcv1.PowerState, error) {
	if t.Protocol == bmcv1.ProtocolIPMI {
		out, err := lanplus(t).Run(ctx, "chassis", "power", "status")
		if err != nil {
			return bmcv1.PowerUnknown, err
		}
		if strings.Contains(string(out), "is on") {
			return bmcv1.PowerOn, nil
		}
		return bmcv1.PowerOff, nil
	}
	sys, c, err := redfishSystem(ctx, t)
	if err != nil {
		return bmcv1.PowerUnknown, err
	}
	defer c.Logout()
	switch sys.PowerState {
	case schemas.OnPowerState, schemas.PoweringOnPowerState:
		return bmcv1.PowerOn, nil
	case schemas.OffPowerState, schemas.PoweringOffPowerState:
		return bmcv1.PowerOff, nil
	}
	return bmcv1.PowerUnknown, nil
}

// Power executes a power action.
func Power(ctx context.Context, t Target, a Action) error {
	if !a.Valid() {
		return fmt.Errorf("unknown action %q", a)
	}
	if t.Protocol == bmcv1.ProtocolIPMI {
		verb, ok := ipmiVerb[a]
		if !ok {
			return fmt.Errorf("action %s is not supported over IPMI", a)
		}
		_, err := lanplus(t).Run(ctx, "chassis", "power", verb)
		return err
	}
	sys, c, err := redfishSystem(ctx, t)
	if err != nil {
		return err
	}
	defer c.Logout()
	_, err = sys.Reset(schemas.ResetType(a))
	return err
}

func lanplus(t Target) ipmi.Exec {
	host, port, err := net.SplitHostPort(t.Address)
	if err != nil {
		host, port = t.Address, "623"
	}
	// -E reads the password from IPMI_PASSWORD so it never shows up in the process list.
	return ipmi.Exec{
		Extra: []string{"-I", "lanplus", "-H", host, "-p", port, "-U", t.Creds.Username, "-E", "-N", "3", "-R", "2"},
		Env:   []string{"IPMI_PASSWORD=" + t.Creds.Password},
	}
}

func redfishSystem(ctx context.Context, t Target) (*schemas.ComputerSystem, *gofish.APIClient, error) {
	endpoint := t.Address
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	c, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint: endpoint, Username: t.Creds.Username, Password: t.Creds.Password,
		Insecure: t.Insecure, BasicAuth: true, TLSHandshakeTimeout: int((10 * time.Second).Seconds()),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("redfish connect %s: %w", t.Address, err)
	}
	systems, err := c.Service.Systems()
	if err != nil {
		c.Logout()
		return nil, nil, fmt.Errorf("redfish %s: list systems: %w", t.Address, err)
	}
	if len(systems) == 0 {
		c.Logout()
		return nil, nil, fmt.Errorf("redfish %s: no computer system found", t.Address)
	}
	return systems[0], c, nil
}
