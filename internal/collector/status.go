package collector

import (
	"maps"
	"math"
	"slices"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
)

// Status converts a snapshot into a BMC status. The result is deterministic, so callers
// can compare results to skip unnecessary writes.
func Status(s *Snapshot) bmcv1.BMCStatus {
	health, sum, problems := Evaluate(s)
	st := bmcv1.BMCStatus{
		Health:     health,
		PowerState: bmcv1.PowerUnknown,
		Device: bmcv1.Device{
			Manufacturer: s.FRU.Manufacturer, Product: s.FRU.Product, Version: s.FRU.Version,
			SerialNumber: s.FRU.SerialNumber, PartNumber: s.FRU.PartNumber, BoardProduct: s.FRU.BoardProduct,
			BoardSerial: s.FRU.BoardSerial, ChassisType: s.FRU.ChassisType, ChassisSerial: s.FRU.ChassisSerial,
		},
		Controller: bmcv1.Controller{
			FirmwareVersion: s.MC.FirmwareVersion, IPMIVersion: s.MC.IPMIVersion,
			ManufacturerID: s.MC.ManufacturerID, ProductID: s.MC.ProductID, GUID: s.GUID,
		},
		Network: bmcv1.Network{
			Channel: s.LAN.Channel, IPAddress: s.LAN.IPAddress, Netmask: s.LAN.Netmask, Gateway: s.LAN.Gateway,
			MACAddress: s.LAN.MACAddress, Source: s.LAN.Source, VLAN: s.LAN.VLAN,
		},
		Chassis: bmcv1.Chassis{
			PowerRestorePolicy: s.Chassis.PowerRestorePolicy, LastPowerEvent: s.Chassis.LastPowerEvent,
			IntrusionActive: s.Chassis.IntrusionActive, Faults: s.Chassis.Faults,
		},
		SEL:      bmcv1.SEL{Entries: s.SELInfo.Entries, UsedPercent: s.SELInfo.UsedPercent, LastAddTime: s.SELInfo.LastAddTime},
		Sensors:  sum,
		Problems: problems,
	}
	if _, failed := s.Errors["chassis"]; !failed && !s.CollectedAt.IsZero() {
		st.PowerState = bmcv1.PowerOff
		if s.Chassis.PowerOn {
			st.PowerState = bmcv1.PowerOn
		}
	}
	if s.PowerWatts >= 0 {
		w := int32(s.PowerWatts)
		st.PowerWatts = &w
	}
	if h := s.Hardware; h != nil {
		hw := &bmcv1.Hardware{
			CPU:    bmcv1.HardwareCPU{Model: h.CPU.Model, Sockets: h.CPU.Sockets, Cores: h.CPU.Cores, Threads: h.CPU.Threads},
			Memory: bmcv1.HardwareMemory{TotalGiB: h.Memory.TotalGiB, Modules: h.Memory.Modules, Slots: h.Memory.Slots, Type: h.Memory.Type, SpeedMTs: h.Memory.SpeedMTs},
		}
		models := h.GPUModels()
		for _, m := range slices.Sorted(maps.Keys(models)) {
			hw.GPUs = append(hw.GPUs, bmcv1.GPUModel{Model: m, Count: models[m]})
		}
		for _, sl := range h.PCIeSlots {
			hw.PCIeSlots = append(hw.PCIeSlots, bmcv1.PCIeSlot{Name: sl.Name, Width: sl.Width, Generation: sl.Generation, InUse: sl.InUse, Device: sl.Device})
		}
		st.Hardware = hw
	}
	if t, ok := InletTemperature(s.Sensors); ok {
		v := int32(math.Round(t))
		st.InletTemperature = &v
	}
	return st
}
