package bmc_test

import (
	"errors"
	"testing"

	"github.com/aireet/kube-bmc/bmc"
)

func TestPowerRejectsUnsupportedActions(t *testing.T) {
	ipmi := bmc.Endpoint{Address: "127.0.0.1", Protocol: bmc.IPMI}
	if err := bmc.Power(t.Context(), ipmi, bmc.GracefulRestart); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("graceful restart over IPMI: %v", err)
	}
	redfish := bmc.Endpoint{Address: "127.0.0.1"}
	if err := bmc.Power(t.Context(), redfish, "Explode"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if err := bmc.Power(t.Context(), bmc.Endpoint{Protocol: "SNMP"}, bmc.On); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}
