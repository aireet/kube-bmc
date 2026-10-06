// Package hostinv reads the hardware inventory of the host the agent runs on: processors,
// memory modules and PCIe slots from SMBIOS (dmidecode), and PCI devices from lspci and
// sysfs. IPMI does not report this information, but the privileged agent can read it from
// the host directly. All commands are read-only.
package hostinv

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Inventory is the hardware of one host.
type Inventory struct {
	CPU       CPU         `json:"cpu"`
	Memory    Memory      `json:"memory"`
	GPUs      []PCIDevice `json:"gpus,omitempty"`
	PCIeSlots []Slot      `json:"pcieSlots,omitempty"`
}

// CPU summarizes the processors.
type CPU struct {
	Model   string `json:"model,omitempty"`
	Sockets int    `json:"sockets,omitempty"`
	Cores   int    `json:"cores,omitempty"`
	Threads int    `json:"threads,omitempty"`
}

// Memory summarizes the memory modules.
type Memory struct {
	TotalGiB  int    `json:"totalGiB,omitempty"`
	Modules   int    `json:"modules,omitempty"`
	Slots     int    `json:"slots,omitempty"`
	Type      string `json:"type,omitempty"`
	SpeedMTs  int    `json:"speedMTs,omitempty"`
	ModuleGiB int    `json:"moduleGiB,omitempty"`
}

// Slot is a physical PCIe slot.
type Slot struct {
	Name       string `json:"name"`
	Width      string `json:"width,omitempty"`
	Generation string `json:"generation,omitempty"`
	InUse      bool   `json:"inUse"`
	BusAddress string `json:"busAddress,omitempty"`
	// Device is the main device installed in the slot, if it could be identified.
	Device string `json:"device,omitempty"`
}

// PCIDevice is a PCI function as reported by lspci.
type PCIDevice struct {
	Address string `json:"address"`
	Class   string `json:"class"`
	Vendor  string `json:"vendor"`
	Model   string `json:"model"`
	Slot    string `json:"slot,omitempty"`
}

// Runner executes a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Resolver returns the sysfs device path of a PCI address, e.g.
// /sys/devices/pci0000:15/0000:15:01.0/0000:16:00.0 for 0000:16:00.0.
type Resolver func(address string) (string, error)

// SysfsResolver resolves PCI addresses through /sys/bus/pci/devices.
func SysfsResolver(address string) (string, error) {
	return filepath.EvalSymlinks("/sys/bus/pci/devices/" + address)
}

// Collect reads the inventory. Sources that fail are skipped, so a partial inventory is
// returned together with the errors.
func Collect(ctx context.Context, run Runner, resolve Resolver) (Inventory, error) {
	var inv Inventory
	var errs []error
	if out, err := run(ctx, "dmidecode", "-t", "processor"); err == nil {
		inv.CPU = ParseProcessors(out)
	} else {
		errs = append(errs, err)
	}
	if out, err := run(ctx, "dmidecode", "-t", "memory"); err == nil {
		inv.Memory = ParseMemory(out)
	} else {
		errs = append(errs, err)
	}
	var devices []PCIDevice
	if out, err := run(ctx, "lspci", "-mm", "-nn", "-D"); err == nil {
		devices = ParseLspci(out)
	} else {
		errs = append(errs, err)
	}
	if out, err := run(ctx, "dmidecode", "-t", "slot"); err == nil {
		inv.PCIeSlots = ParseSlots(out)
	} else {
		errs = append(errs, err)
	}
	inv.PCIeSlots, devices = assignSlots(inv.PCIeSlots, devices, resolve)
	for _, d := range devices {
		if isGPU(d) {
			inv.GPUs = append(inv.GPUs, d)
		}
	}
	if len(errs) > 0 {
		return inv, fmt.Errorf("hardware inventory: %v", errs)
	}
	return inv, nil
}

// blocks splits dmidecode output into the key/value sets of each structure.
func blocks(out []byte) []map[string]string {
	var res []map[string]string
	var cur map[string]string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "Handle ") {
			cur = map[string]string{}
			res = append(res, cur)
			continue
		}
		if cur == nil || !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return res
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.Fields(s + " 0")[0])
	return n
}

// ParseProcessors parses `dmidecode -t processor`.
func ParseProcessors(out []byte) CPU {
	var c CPU
	for _, b := range blocks(out) {
		if b["Status"] != "" && !strings.Contains(b["Status"], "Populated") {
			continue
		}
		if c.Model == "" {
			c.Model = cleanCPUModel(b["Version"])
		}
		c.Sockets++
		c.Cores += atoi(b["Core Count"])
		c.Threads += atoi(b["Thread Count"])
	}
	return c
}

var cpuNoise = regexp.MustCompile(`\((R|TM|tm|r)\)|\s+CPU\b|\s+@.*$|\s+Processor$|\s+\d+-Core.*$`)

func cleanCPUModel(s string) string {
	return strings.Join(strings.Fields(cpuNoise.ReplaceAllString(s, "")), " ")
}

// ParseMemory parses `dmidecode -t memory`.
func ParseMemory(out []byte) Memory {
	var m Memory
	for _, b := range blocks(out) {
		if _, ok := b["Locator"]; !ok {
			continue // Physical Memory Array
		}
		m.Slots++
		size := b["Size"]
		if size == "" || strings.HasPrefix(size, "No Module") || strings.HasPrefix(size, "Not Installed") {
			continue
		}
		gib := sizeGiB(size)
		m.Modules++
		m.TotalGiB += gib
		if m.ModuleGiB == 0 {
			m.ModuleGiB = gib
		}
		if t := b["Type"]; m.Type == "" && t != "" && t != "Unknown" && t != "Other" {
			m.Type = t
		}
		if s := atoi(b["Configured Memory Speed"]); s > 0 && (m.SpeedMTs == 0 || s < m.SpeedMTs) {
			m.SpeedMTs = s
		} else if s := atoi(b["Speed"]); m.SpeedMTs == 0 && s > 0 {
			m.SpeedMTs = s
		}
	}
	return m
}

func sizeGiB(s string) int {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	n := atoi(f[0])
	switch f[1] {
	case "TB":
		return n * 1024
	case "MB":
		return n / 1024
	}
	return n
}

// dmidecode 3.3 prints "Type: x16 PCI Express 5 x16"; 3.5 and later print
// "Type: PCI Express 5 x16" and "Data Bus Width: 16x or x16".
var (
	slotGeneration = regexp.MustCompile(`PCI Express\s+(\d)\b`)
	slotWidth      = regexp.MustCompile(`\bx(\d+)\b`)
)

// ParseSlots parses `dmidecode -t slot` and returns the PCIe slots.
func ParseSlots(out []byte) []Slot {
	var res []Slot
	for _, b := range blocks(out) {
		t := b["Type"]
		if !strings.Contains(t, "PCI Express") {
			continue
		}
		s := Slot{Name: b["Designation"], InUse: b["Current Usage"] == "In Use", BusAddress: strings.ToLower(b["Bus Address"])}
		if m := slotGeneration.FindStringSubmatch(t); m != nil {
			s.Generation = "Gen" + m[1]
		}
		if m := slotWidth.FindStringSubmatch(t); m != nil {
			s.Width = "x" + m[1]
		} else if m := slotWidth.FindStringSubmatch(b["Data Bus Width"]); m != nil {
			s.Width = "x" + m[1]
		}
		res = append(res, s)
	}
	slices.SortStableFunc(res, func(a, b Slot) int { return naturalCompare(a.Name, b.Name) })
	return res
}

var lspciField = regexp.MustCompile(`"([^"]*)"`)

// ParseLspci parses `lspci -mm -nn -D`.
func ParseLspci(out []byte) []PCIDevice {
	var res []PCIDevice
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		addr, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		f := lspciField.FindAllStringSubmatch(rest, -1)
		if len(f) < 3 {
			continue
		}
		res = append(res, PCIDevice{Address: strings.ToLower(addr), Class: f[0][1], Vendor: f[1][1], Model: f[2][1]})
	}
	return res
}

// vendorSuffixes are removed from PCI vendor names, in this order.
var vendorSuffixes = []string{", Inc.", " Inc.", " Co., Ltd.", " Corporation", " Technologies", " Technology"}

var bracketID = regexp.MustCompile(`\s*\[([0-9a-f]{4})\]$`)

// name returns a readable device name, e.g. "NVIDIA GB202 [GeForce RTX 5090]", or the
// vendor and device IDs when the PCI ID database does not know the device.
func (d PCIDevice) name() string {
	vendor := bracketID.ReplaceAllString(d.Vendor, "")
	for _, suffix := range vendorSuffixes {
		vendor = strings.TrimSuffix(vendor, suffix)
	}
	model := d.Model
	if strings.HasPrefix(model, "Device [") {
		vid := bracketID.FindStringSubmatch(d.Vendor)
		did := bracketID.FindStringSubmatch(model)
		if vid != nil && did != nil {
			return fmt.Sprintf("%s device %s:%s", vendor, vid[1], did[1])
		}
	}
	return vendor + " " + bracketID.ReplaceAllString(model, "")
}

func classCode(c string) string {
	if m := bracketID.FindStringSubmatch(c); m != nil {
		return m[1]
	}
	return ""
}

func isGPU(d PCIDevice) bool {
	code := classCode(d.Class)
	if !strings.HasPrefix(code, "03") {
		return false
	}
	// Exclude the BMC's own display controller.
	return !strings.Contains(d.Vendor, "ASPEED") && !strings.Contains(d.Vendor, "Matrox")
}

// isEndpoint reports whether a device is worth naming in a slot: not a bridge.
func isEndpoint(d PCIDevice) bool {
	return !strings.HasPrefix(classCode(d.Class), "06")
}

// assignSlots finds the devices behind each slot through the sysfs topology: a device
// belongs to a slot when the slot's bus address appears in its device path.
func assignSlots(slots []Slot, devices []PCIDevice, resolve Resolver) ([]Slot, []PCIDevice) {
	if resolve == nil {
		return slots, devices
	}
	for i := range devices {
		path, err := resolve(devices[i].Address)
		if err != nil {
			continue
		}
		for _, s := range slots {
			if s.BusAddress != "" && s.BusAddress != devices[i].Address && strings.Contains(path, "/"+s.BusAddress+"/") {
				devices[i].Slot = s.Name
				break
			}
		}
	}
	for i := range slots {
		for _, d := range devices {
			if d.Slot == slots[i].Name && isEndpoint(d) && strings.HasSuffix(d.Address, ".0") {
				slots[i].Device = d.name()
				slots[i].InUse = true
				break
			}
		}
	}
	return slots, devices
}

var digits = regexp.MustCompile(`\d+|\D+`)

// naturalCompare orders "PCIE2" before "PCIE10".
func naturalCompare(a, b string) int {
	pa, pb := digits.FindAllString(a, -1), digits.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil && na != nb:
			return na - nb
		case pa[i] != pb[i] && (ea != nil || eb != nil):
			return strings.Compare(pa[i], pb[i])
		}
	}
	return len(pa) - len(pb)
}

// GPUModels groups GPUs by model name, e.g. {"NVIDIA GB202 [GeForce RTX 5090]": 8}.
func (inv Inventory) GPUModels() map[string]int {
	res := map[string]int{}
	for _, g := range inv.GPUs {
		if strings.HasSuffix(g.Address, ".0") {
			res[g.name()]++
		}
	}
	return res
}
