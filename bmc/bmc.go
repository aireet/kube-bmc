// Package bmc performs power operations on a server through its baseboard management
// controller over the network, with Redfish or IPMI. The operation works whatever the
// state of the server's operating system, including when the server is off.
//
//	e := bmc.Endpoint{Address: "10.0.0.7", Username: "admin", Password: pw, Insecure: true}
//	err := bmc.Power(ctx, e, bmc.PowerCycle)
package bmc

import (
	"context"
	"errors"
	"fmt"

	"github.com/aireet/kube-bmc/ipmi"
	"github.com/aireet/kube-bmc/redfish"
)

// Protocol is a BMC management protocol.
type Protocol string

const (
	Redfish Protocol = "Redfish"
	IPMI    Protocol = "IPMI" // IPMI 2.0 over LAN (lanplus)
)

// Endpoint describes how to reach a BMC over the network.
type Endpoint struct {
	// Address is the host of the BMC, with an optional port, or for Redfish its URL.
	Address string
	// Protocol defaults to Redfish.
	Protocol Protocol
	Username string
	Password string
	// Insecure skips verification of the BMC's TLS certificate (Redfish only).
	Insecure bool
}

// PowerAction is a power operation, named after the Redfish reset types.
type PowerAction string

const (
	On               PowerAction = "On"
	ForceOff         PowerAction = "ForceOff"
	GracefulShutdown PowerAction = "GracefulShutdown"
	GracefulRestart  PowerAction = "GracefulRestart"
	ForceRestart     PowerAction = "ForceRestart"
	PowerCycle       PowerAction = "PowerCycle"
)

// ipmiActions maps power actions to IPMI chassis control. IPMI has no graceful restart.
var ipmiActions = map[PowerAction]ipmi.PowerAction{
	On:               ipmi.PowerOn,
	ForceOff:         ipmi.PowerOff,
	GracefulShutdown: ipmi.PowerSoftOff,
	ForceRestart:     ipmi.PowerReset,
	PowerCycle:       ipmi.PowerCycle,
}

// Power performs the power action a on the server managed by the BMC at e. Actions a
// protocol cannot perform, such as GracefulRestart over IPMI, return an error wrapping
// errors.ErrUnsupported.
func Power(ctx context.Context, e Endpoint, a PowerAction) error {
	switch a {
	case On, ForceOff, GracefulShutdown, GracefulRestart, ForceRestart, PowerCycle:
	default:
		return fmt.Errorf("unknown power action %q", a)
	}
	switch e.Protocol {
	case IPMI:
		op, ok := ipmiActions[a]
		if !ok {
			return fmt.Errorf("%w: %s over IPMI", errors.ErrUnsupported, a)
		}
		c := ipmi.New(ipmi.Tool{Host: e.Address, Username: e.Username, Password: e.Password})
		return c.SetPower(ctx, op)
	case Redfish, "":
		c, err := redfish.Dial(ctx, redfish.Config{Endpoint: e.Address, Username: e.Username, Password: e.Password, Insecure: e.Insecure})
		if err != nil {
			return err
		}
		defer c.Close()
		return c.Reset(ctx, redfish.ResetType(a))
	}
	return fmt.Errorf("unknown protocol %q", e.Protocol)
}
