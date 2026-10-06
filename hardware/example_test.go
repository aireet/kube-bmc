package hardware_test

import (
	"context"
	"fmt"
	"log"

	"github.com/aireet/kube-bmc/hardware"
)

// Print the GPUs of the local host and the PCIe slots that are free.
func ExampleRead() {
	inv, err := hardware.Read(context.Background())
	if err != nil {
		log.Print(err) // a partial inventory is still returned
	}
	for model, n := range inv.GPUModels() {
		fmt.Printf("%d× %s\n", n, model)
	}
	for _, s := range inv.PCIeSlots {
		if !s.InUse {
			fmt.Printf("%s (%s %s) is free\n", s.Name, s.Generation, s.Width)
		}
	}
}
