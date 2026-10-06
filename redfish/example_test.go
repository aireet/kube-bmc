package redfish_test

import (
	"context"
	"fmt"
	"log"

	"github.com/aireet/kube-bmc/redfish"
)

func ExampleClient_PowerState() {
	ctx := context.Background()
	c, err := redfish.Dial(ctx, redfish.Config{Endpoint: "10.0.0.7", Username: "admin", Password: "secret", Insecure: true})
	if err != nil {
		log.Fatal(err)
	}
	state, err := c.PowerState(ctx)
	_ = c.Close()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(state)
}
