package bmc_test

import (
	"context"
	"errors"
	"log"

	"github.com/aireet/kube-bmc/bmc"
)

// Power cycle a server through the Redfish service of its BMC, falling back to IPMI.
func ExamplePower() {
	ctx := context.Background()
	e := bmc.Endpoint{Address: "10.0.0.7", Username: "admin", Password: "secret", Insecure: true}
	err := bmc.Power(ctx, e, bmc.PowerCycle)
	if err != nil && !errors.Is(err, errors.ErrUnsupported) {
		e.Protocol = bmc.IPMI
		err = bmc.Power(ctx, e, bmc.PowerCycle)
	}
	if err != nil {
		log.Fatal(err)
	}
}
