package ipmi_test

import (
	"context"
	"fmt"
	"log"

	"github.com/aireet/kube-bmc/ipmi"
	"github.com/aireet/kube-bmc/ipmi/ipmitest"
)

// List the failing sensors of a server. In production, ipmi.Tool{} reaches the BMC of
// the local host; this example replays the recorded output of a server with failed fans.
func Example() {
	c := ipmi.New(ipmitest.Recorded()) // ipmi.New(ipmi.Tool{})
	sensors, err := c.Sensors(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range sensors {
		if s.Severity == ipmi.SeverityCritical {
			fmt.Printf("%s: %s (%s)\n", s.Name, s.Reading, s.Status)
		}
	}
	// Output:
	// FAN11: 0 RPM (lnr)
	// FAN7: 0 RPM (lnr)
	// FAN10: 0 RPM (lnr)
	// FAN12: 0 RPM (lnr)
	// FAN8: 0 RPM (lnr)
}

// Read the configuration of a remote BMC over the network.
func ExampleTool_remote() {
	c := ipmi.New(ipmi.Tool{Host: "10.0.0.7", Username: "admin", Password: "secret"})
	lan, err := c.LAN(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(lan.MACAddress)
}

// Turn the identify light on so that data-center staff can find the server.
func ExampleClient_Identify() {
	c := ipmi.New(ipmi.Tool{})
	ctx := context.Background()
	if err := c.Identify(ctx, ipmi.IdentifyIndefinitely); err != nil {
		// Some BMCs only support timed identify.
		err = c.Identify(ctx, ipmi.MaxIdentify)
		if err != nil {
			log.Fatal(err)
		}
	}
}
