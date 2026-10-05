// Package oob executes power operations against a BMC over its management network,
// independently of the state of the host operating system.
package oob

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

// ipmiVerb maps actions to `ipmitool chassis power` sub-commands. IPMI has no
// graceful restart, so GracefulRestart is only available over Redfish.
var ipmiVerb = map[bmcv1.ActionType]string{
	bmcv1.ActionOn:               "on",
	bmcv1.ActionGracefulShutdown: "soft",
	bmcv1.ActionForceOff:         "off",
	bmcv1.ActionForceRestart:     "reset",
	bmcv1.ActionPowerCycle:       "cycle",
}

// Credentials authenticate against the BMC.
type Credentials struct{ Username, Password string }

// Target identifies a BMC endpoint.
type Target struct {
	Address  string
	Protocol bmcv1.Protocol
	Insecure bool
	Creds    Credentials
}

// TargetFor resolves the endpoint of a BMC. spec.address takes precedence over the
// address discovered in-band by the node agent.
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

// Power executes a power action.
func Power(ctx context.Context, t Target, a bmcv1.ActionType) error {
	if !a.Valid() {
		return fmt.Errorf("unsupported action %q", a)
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
	if _, err := sys.Reset(schemas.ResetType(a)); err != nil {
		return fmt.Errorf("redfish %s: reset %s: %w", t.Address, a, err)
	}
	return nil
}

func lanplus(t Target) ipmi.Exec {
	host, port, err := net.SplitHostPort(t.Address)
	if err != nil {
		host, port = t.Address, "623"
	}
	// -E reads the password from IPMI_PASSWORD instead of the command line.
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
		Endpoint:            endpoint,
		Username:            t.Creds.Username,
		Password:            t.Creds.Password,
		Insecure:            t.Insecure,
		BasicAuth:           true,
		TLSHandshakeTimeout: 10,
		// gofish applies Insecure and the handshake timeout only to an *http.Transport.
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("redfish %s: connect: %w", t.Address, err)
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
