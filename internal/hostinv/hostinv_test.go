package hostinv

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Fixtures are real output from an 8-GPU server (2x Xeon Platinum 8468V), with serial
// numbers removed.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sysfs(t *testing.T) Resolver {
	paths := map[string]string{}
	for _, line := range strings.Split(string(fixture(t, "sysfs_paths.txt")), "\n") {
		if addr, path, ok := strings.Cut(line, " "); ok {
			paths[addr] = path
		}
	}
	return func(addr string) (string, error) {
		if p, ok := paths[addr]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
}

func TestParseProcessors(t *testing.T) {
	c := ParseProcessors(fixture(t, "dmidecode_processor.txt"))
	if c.Model != "Intel Xeon Platinum 8468V" || c.Sockets != 2 || c.Cores != 96 || c.Threads != 192 {
		t.Fatalf("cpu = %+v", c)
	}
}

func TestParseMemory(t *testing.T) {
	m := ParseMemory(fixture(t, "dmidecode_memory.txt"))
	if m.Slots != 32 || m.Modules != 8 || m.TotalGiB != 512 || m.ModuleGiB != 64 || m.Type != "DDR5" || m.SpeedMTs == 0 {
		t.Fatalf("memory = %+v", m)
	}
}

func TestCollect(t *testing.T) {
	files := map[string]string{
		"dmidecode -t processor": "dmidecode_processor.txt", "dmidecode -t memory": "dmidecode_memory.txt",
		"dmidecode -t slot": "dmidecode_slot.txt", "lspci -mm -nn -D": "lspci.txt",
	}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		return fixture(t, files[name+" "+strings.Join(args, " ")]), nil
	}
	inv, err := Collect(context.Background(), run, sysfs(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.GPUs) != 8 {
		t.Fatalf("gpus = %d", len(inv.GPUs))
	}
	models := inv.GPUModels()
	if models["NVIDIA device 10de:2b85"] != 8 || len(models) != 1 {
		t.Fatalf("gpu models = %v", models)
	}
	var pcie5 *Slot
	used := 0
	for i, s := range inv.PCIeSlots {
		if s.Name == "PCIE5" {
			pcie5 = &inv.PCIeSlots[i]
		}
		if s.InUse {
			used++
		}
	}
	if pcie5 == nil || !pcie5.InUse || pcie5.Width != "x16" || pcie5.Generation != "Gen5" || pcie5.Device != "NVIDIA device 10de:2b85" {
		t.Fatalf("PCIE5 = %+v", pcie5)
	}
	if used < 8 || used == len(inv.PCIeSlots) {
		t.Fatalf("%d of %d slots in use", used, len(inv.PCIeSlots))
	}
	for i := 1; i < len(inv.PCIeSlots); i++ {
		if naturalCompare(inv.PCIeSlots[i-1].Name, inv.PCIeSlots[i].Name) > 0 {
			t.Fatalf("slots not sorted: %s before %s", inv.PCIeSlots[i-1].Name, inv.PCIeSlots[i].Name)
		}
	}
}

func TestCollectPartial(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dmidecode" && args[1] == "processor" {
			return fixture(t, "dmidecode_processor.txt"), nil
		}
		return nil, errors.New("not available")
	}
	inv, err := Collect(context.Background(), run, nil)
	if err == nil || inv.CPU.Sockets != 2 {
		t.Fatalf("inv = %+v, err = %v", inv, err)
	}
}

func TestDeviceName(t *testing.T) {
	for _, tc := range []struct {
		d    PCIDevice
		want string
	}{
		{PCIDevice{Vendor: "NVIDIA Corporation [10de]", Model: "GB202 [GeForce RTX 5090] [2b85]"}, "NVIDIA GB202 [GeForce RTX 5090]"},
		{PCIDevice{Vendor: "NVIDIA Corporation [10de]", Model: "Device [2b85]"}, "NVIDIA device 10de:2b85"},
		{PCIDevice{Vendor: "Mellanox Technologies [15b3]", Model: "MT2910 Family [ConnectX-7] [1021]"}, "Mellanox MT2910 Family [ConnectX-7]"},
	} {
		if got := tc.d.name(); got != tc.want {
			t.Errorf("name(%+v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if !isGPU(PCIDevice{Class: "3D controller [0302]", Vendor: "NVIDIA Corporation [10de]"}) ||
		isGPU(PCIDevice{Class: "VGA compatible controller [0300]", Vendor: "ASPEED Technology, Inc. [1a03]"}) {
		t.Fatal("GPU classification")
	}
}

func TestParseSlotsNewerDmidecode(t *testing.T) {
	out := []byte("Handle 0x0901, DMI type 9, 24 bytes\nSystem Slot Information\n\tDesignation: PCIE2\n\tType: PCI Express 5 x16\n" +
		"\tData Bus Width: 16x or x16\n\tCurrent Usage: In Use\n\tBus Address: 0000:a7:01.0\n\n" +
		"Handle 0x0902, DMI type 9, 24 bytes\nSystem Slot Information\n\tDesignation: PCIE4\n\tType: PCI Express 4\n" +
		"\tData Bus Width: 8x or x8\n\tCurrent Usage: Available\n\tBus Address: 0000:59:05.0\n")
	slots := ParseSlots(out)
	if len(slots) != 2 || slots[0].Width != "x16" || slots[0].Generation != "Gen5" || !slots[0].InUse ||
		slots[1].Width != "x8" || slots[1].Generation != "Gen4" || slots[1].InUse {
		t.Fatalf("slots = %+v", slots)
	}
}

// The epyc fixtures come from a server whose firmware reports the card itself, not the port
// above it, as the slot's bus address. All ten slots are populated: eight GPUs, a RAID
// controller and a network adapter.
func TestCollectSlotAddressIsCard(t *testing.T) {
	paths := map[string]string{}
	for _, line := range strings.Split(string(fixture(t, "epyc/sysfs_paths.txt")), "\n") {
		if addr, path, ok := strings.Cut(line, " "); ok {
			paths[addr] = path
		}
	}
	resolve := func(addr string) (string, error) {
		if p, ok := paths[addr]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	files := map[string]string{"dmidecode -t slot": "epyc/dmidecode_slot.txt", "lspci -mm -nn -D": "epyc/lspci.txt"}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if f, ok := files[name+" "+strings.Join(args, " ")]; ok {
			return fixture(t, f), nil
		}
		return nil, errors.New("not available")
	}
	inv, _ := Collect(context.Background(), run, resolve)
	if len(inv.PCIeSlots) != 10 {
		t.Fatalf("slots = %d", len(inv.PCIeSlots))
	}
	devices := map[string]string{}
	for _, s := range inv.PCIeSlots {
		if !s.InUse || s.Device == "" {
			t.Errorf("%s: in use %v, device %q", s.Name, s.InUse, s.Device)
		}
		devices[s.Name] = s.Device
	}
	if !strings.Contains(devices["SLOT9"], "RTX 4090") || !strings.Contains(devices["SLOT5"], "MegaRAID") ||
		!strings.Contains(devices["SLOT6"], "ConnectX-4") {
		t.Fatalf("devices = %v", devices)
	}
	for _, g := range inv.GPUs {
		if g.Slot == "" {
			t.Errorf("gpu %s has no slot", g.Address)
		}
	}
}
