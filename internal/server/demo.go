package server

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/ipmi"
	"github.com/aireet/kube-bmc/internal/oob"
)

// Demo serves a synthetic fleet so the dashboard can be explored without a cluster or hardware.
type Demo struct {
	mu      sync.Mutex
	started time.Time
	power   map[string]bool
}

func NewDemo() *Demo {
	d := &Demo{started: time.Now(), power: map[string]bool{}}
	for _, m := range demoFleet {
		d.power[m.name] = !m.off
	}
	return d
}

type demoMachine struct {
	name, vendor, product, bmcFW, gpuModel, role string
	gpus, fans, psus                             int
	baseWatts                                    int
	off, stale, hotInlet, deadFan, selFull, psu  bool
}

var demoFleet = []demoMachine{
	{name: "gpu-a100-01", vendor: "Supermicro", product: "SYS-821GE-TNHR", bmcFW: "01.03.12", gpuModel: "NVIDIA-H100-80GB-HBM3", role: "worker", gpus: 8, fans: 10, psus: 4, baseWatts: 6200},
	{name: "gpu-a100-02", vendor: "Supermicro", product: "SYS-821GE-TNHR", bmcFW: "01.03.12", gpuModel: "NVIDIA-H100-80GB-HBM3", role: "worker", gpus: 8, fans: 10, psus: 4, baseWatts: 5900, hotInlet: true},
	{name: "gpu-h200-01", vendor: "Dell Inc.", product: "PowerEdge XE9680", bmcFW: "7.10.50.00", gpuModel: "NVIDIA-H200", role: "worker", gpus: 8, fans: 16, psus: 6, baseWatts: 7400},
	{name: "gpu-h200-02", vendor: "Dell Inc.", product: "PowerEdge XE9680", bmcFW: "7.10.50.00", gpuModel: "NVIDIA-H200", role: "worker", gpus: 8, fans: 16, psus: 6, baseWatts: 7100, deadFan: true},
	{name: "gpu-l40s-01", vendor: "HPE", product: "ProLiant DL380a Gen11", bmcFW: "iLO 6 v1.62", gpuModel: "NVIDIA-L40S", role: "worker", gpus: 4, fans: 6, psus: 2, baseWatts: 2300},
	{name: "gpu-l40s-02", vendor: "HPE", product: "ProLiant DL380a Gen11", bmcFW: "iLO 6 v1.62", gpuModel: "NVIDIA-L40S", role: "worker", gpus: 4, fans: 6, psus: 2, baseWatts: 2250, selFull: true},
	{name: "gpu-5090-01", vendor: "Gooxi", product: "SY8108G-G4", bmcFW: "2.18", gpuModel: "NVIDIA-GeForce-RTX-5090", role: "worker", gpus: 8, fans: 12, psus: 4, baseWatts: 4100},
	{name: "gpu-5090-02", vendor: "Gooxi", product: "SY8108G-G4", bmcFW: "2.18", gpuModel: "NVIDIA-GeForce-RTX-5090", role: "worker", gpus: 8, fans: 12, psus: 4, baseWatts: 3900, psu: true},
	{name: "infer-01", vendor: "Lenovo", product: "ThinkSystem SR675 V3", bmcFW: "XCC 3.10", gpuModel: "NVIDIA-L4", role: "worker", gpus: 4, fans: 6, psus: 2, baseWatts: 1100},
	{name: "infer-02", vendor: "Lenovo", product: "ThinkSystem SR675 V3", bmcFW: "XCC 3.10", gpuModel: "NVIDIA-L4", role: "worker", gpus: 4, fans: 6, psus: 2, baseWatts: 0, off: true},
	{name: "cpu-01", vendor: "Inspur", product: "NF5280M7", bmcFW: "4.32.0", role: "worker", fans: 6, psus: 2, baseWatts: 520},
	{name: "cpu-02", vendor: "Inspur", product: "NF5280M7", bmcFW: "4.32.0", role: "worker", fans: 6, psus: 2, baseWatts: 480, stale: true},
	{name: "cp-01", vendor: "Dell Inc.", product: "PowerEdge R660", bmcFW: "7.10.30.00", role: "control-plane", fans: 8, psus: 2, baseWatts: 310},
	{name: "cp-02", vendor: "Dell Inc.", product: "PowerEdge R660", bmcFW: "7.10.30.00", role: "control-plane", fans: 8, psus: 2, baseWatts: 295},
	{name: "cp-03", vendor: "Dell Inc.", product: "PowerEdge R660", bmcFW: "7.10.30.00", role: "control-plane", fans: 8, psus: 2, baseWatts: 320},
}

func (d *Demo) find(name string) (demoMachine, error) {
	for _, m := range demoFleet {
		if m.name == name {
			return m, nil
		}
	}
	return demoMachine{}, fmt.Errorf("bmc %s: %w", name, ErrNotFound)
}

func (d *Demo) List(context.Context) ([]View, error) {
	views := make([]View, 0, len(demoFleet))
	for i, m := range demoFleet {
		views = append(views, d.view(i, m))
	}
	return views, nil
}

func (d *Demo) Get(_ context.Context, name string) (View, error) {
	for i, m := range demoFleet {
		if m.name == name {
			return d.view(i, m), nil
		}
	}
	return View{}, fmt.Errorf("bmc %s: %w", name, ErrNotFound)
}

func (d *Demo) Live(_ context.Context, name string) (*collector.Snapshot, error) {
	m, err := d.find(name)
	if err != nil {
		return nil, err
	}
	if m.stale {
		return nil, fmt.Errorf("no running agent on node %s", name)
	}
	s := d.snapshot(m)
	return &s, nil
}

func (d *Demo) PowerState(_ context.Context, name string) (bmcv1.PowerState, error) {
	if _, err := d.find(name); err != nil {
		return bmcv1.PowerUnknown, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.power[name] {
		return bmcv1.PowerOn, nil
	}
	return bmcv1.PowerOff, nil
}

func (d *Demo) Power(_ context.Context, name string, a oob.Action, _ string) error {
	if _, err := d.find(name); err != nil {
		return err
	}
	time.Sleep(800 * time.Millisecond)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch a {
	case oob.On, oob.GracefulRestart, oob.ForceRestart, oob.PowerCycle:
		d.power[name] = true
	case oob.GracefulShutdown, oob.ForceOff:
		d.power[name] = false
	}
	return nil
}

func seed(name string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return h.Sum64()
}

func (d *Demo) on(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.power[name]
}

func (d *Demo) snapshot(m demoMachine) collector.Snapshot {
	on := d.on(m.name)
	r := rand.New(rand.NewPCG(seed(m.name), uint64(time.Now().Unix()/5)))
	s0 := seed(m.name)
	octet := 10 + int(s0%200)
	jit := func(base, spread float64) float64 { return math.Round((base+(r.Float64()*2-1)*spread)*10) / 10 }

	var sensors []ipmi.Sensor
	add := func(name, unit string, v float64, status string, th *ipmi.Thresholds) {
		vv := v
		sev := map[string]ipmi.Severity{"ok": ipmi.SeverityOK, "unc": ipmi.SeverityWarning, "lnr": ipmi.SeverityCritical, "ucr": ipmi.SeverityCritical, "ns": ipmi.SeverityNoReading}[status]
		sn := ipmi.Sensor{Name: name, Unit: unit, Status: status, Severity: sev, Thresholds: th}
		switch unit {
		case "degrees C":
			sn.Type = "temperature"
		case "RPM":
			sn.Type = "fan"
		case "Volts":
			sn.Type = "voltage"
		case "Watts":
			sn.Type = "power"
		case "Amps":
			sn.Type = "current"
		default:
			sn.Type = "discrete"
		}
		switch {
		case status == "ns":
			sn.Reading = "No Reading"
		case sn.Type == "discrete":
			sn.Reading, sn.Unit = unit, ""
			sn.Severity = ipmi.DiscreteSeverity(sn.Reading)
		default:
			sn.Value = &vv
			sn.Reading = fmt.Sprintf("%g %s", vv, unit)
		}
		sensors = append(sensors, sn)
	}
	f := func(v float64) *float64 { return &v }
	tempTh := func(unc, ucr, unr float64) *ipmi.Thresholds {
		return &ipmi.Thresholds{UpperNonCritical: f(unc), UpperCritical: f(ucr), UpperNonRecoverable: f(unr)}
	}
	load := 0.0
	if on {
		load = 1
	}

	inlet := jit(23, 1.5)
	inletStatus := "ok"
	if m.hotInlet {
		inlet = jit(43, 1)
		inletStatus = "unc"
	}
	add("Inlet_Temp", "degrees C", inlet, inletStatus, tempTh(40, 50, 65))
	add("Outlet_Temp", "degrees C", jit(inlet+12*load+3, 2), "ok", tempTh(70, 80, 90))
	for i := range 2 {
		add(fmt.Sprintf("CPU%d_Temp", i), "degrees C", jit(35+30*load, 4), "ok", tempTh(88, 95, 100))
		add(fmt.Sprintf("CPU%d_VR_Temp", i), "degrees C", jit(32+20*load, 3), "ok", tempTh(95, 105, 115))
	}
	for i := 1; i <= m.gpus; i++ {
		add(fmt.Sprintf("GPU%d_Temp", i), "degrees C", jit(30+40*load, 5), "ok", tempTh(85, 92, 100))
	}
	for i := 1; i <= 4; i++ {
		add(fmt.Sprintf("DIMM_%c_Temp", 'A'+i-1), "degrees C", jit(28+15*load, 2), "ok", tempTh(80, 85, 90))
	}
	add("PCH_Temp", "degrees C", jit(40+10*load, 2), "ok", tempTh(90, 95, 100))
	add("OCP_NIC_Temp", "degrees C", 0, "ns", nil)

	fanTh := &ipmi.Thresholds{LowerNonRecoverable: f(300), LowerCritical: f(500), LowerNonCritical: f(700)}
	for i := 1; i <= m.fans; i++ {
		if m.deadFan && i == 3 {
			add(fmt.Sprintf("FAN%d", i), "RPM", 0, "lnr", fanTh)
			continue
		}
		add(fmt.Sprintf("FAN%d", i), "RPM", math.Round(jit(4200+6000*load, 400)/60)*60, "ok", fanTh)
	}
	watts := float64(m.baseWatts)
	if !on {
		watts = 18
	}
	for i := 1; i <= m.psus; i++ {
		if m.psu && i == 2 {
			add(fmt.Sprintf("PSU%d_PIN", i), "Watts", 0, "ns", nil)
			add(fmt.Sprintf("PSU%d_Status", i), "Power Supply AC lost", 0, "ok", nil)
			continue
		}
		div := float64(m.psus)
		if m.psu {
			div--
		}
		add(fmt.Sprintf("PSU%d_PIN", i), "Watts", math.Round(jit(watts/div, watts/div*0.04)), "ok", nil)
		add(fmt.Sprintf("PSU%d_Status", i), "Presence detected", 0, "ok", nil)
	}
	vTh := func(n float64) *ipmi.Thresholds {
		return &ipmi.Thresholds{LowerCritical: f(n * 0.9), LowerNonCritical: f(n * 0.95), UpperNonCritical: f(n * 1.05), UpperCritical: f(n * 1.1)}
	}
	add("P12V", "Volts", jit(12.1, 0.08), "ok", vTh(12))
	add("P5V", "Volts", jit(5.0, 0.04), "ok", vTh(5))
	add("P3V3", "Volts", jit(3.3, 0.02), "ok", vTh(3.3))
	add("P3V_BAT", "Volts", jit(3.05, 0.02), "ok", vTh(3))
	add("PVCCIN_CPU0", "Volts", jit(1.8, 0.02), "ok", vTh(1.8))
	add("PVCCIN_CPU1", "Volts", jit(1.8, 0.02), "ok", vTh(1.8))
	add("ChassisIntrusion", "", 0, "ok", nil)

	selPct, selEntries := 4+int(s0%30), 120+int(s0%800)
	if m.selFull {
		selPct, selEntries = 100, 3639
	}
	chassis := ipmi.Chassis{PowerOn: on, PowerRestorePolicy: "previous", LastPowerEvent: "command"}
	if m.deadFan {
		chassis.Faults = []string{"Cooling/Fan Fault"}
	}

	pw := -1
	if m.baseWatts > 0 || !on {
		pw = int(math.Round(jit(watts, watts*0.03)))
	}
	vendorIDs := map[string]string{"Supermicro": "10876", "Dell Inc.": "674", "HPE": "11", "Lenovo": "19046", "Inspur": "37945", "Gooxi": "0"}
	return collector.Snapshot{
		Node:        m.name,
		CollectedAt: time.Now().Truncate(5 * time.Second),
		MC:          ipmi.MCInfo{FirmwareVersion: m.bmcFW, IPMIVersion: "2.0", ManufacturerID: vendorIDs[m.vendor], ManufacturerName: m.vendor},
		GUID:        fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", s0>>32, s0>>16&0xffff, s0&0xffff, s0>>48, s0&0xffffffffffff),
		LAN: ipmi.LAN{Channel: 1, IPAddress: fmt.Sprintf("10.20.0.%d", octet), Netmask: "255.255.255.0", Gateway: "10.20.0.1",
			MACAddress: fmt.Sprintf("3c:ec:ef:%02x:%02x:%02x", s0&0xff, s0>>8&0xff, s0>>16&0xff), Source: "Static Address", VLAN: "Disabled"},
		FRU: ipmi.FRU{Manufacturer: m.vendor, Product: m.product, Version: "1.0",
			SerialNumber: fmt.Sprintf("S%010d", s0%1e10), PartNumber: fmt.Sprintf("PN-%06d", s0%1e6),
			BoardProduct: m.product + " Mainboard", BoardSerial: fmt.Sprintf("B%012d", s0%1e12), ChassisType: "Rack Mount Chassis"},
		Chassis:    chassis,
		SELInfo:    ipmi.SELInfo{Entries: selEntries, UsedPercent: selPct, LastAddTime: time.Now().Add(-37 * time.Minute).Format("01/02/2006 15:04:05")},
		PowerWatts: pw,
		Sensors:    sensors,
		Events:     demoEvents(m, d.started),
		Errors:     map[string]string{},
	}
}

func demoEvents(m demoMachine, t0 time.Time) []ipmi.Event {
	type ev struct {
		ago           time.Duration
		sensor, event string
		asserted      bool
		detail        string
	}
	evs := []ev{
		{-26 * time.Hour, "System Boot Initiated #0x7c", "Initiated by power up", true, ""},
		{-26*time.Hour + 2*time.Minute, "OS Boot #0x7d", "C: boot completed", true, ""},
		{-9 * 24 * time.Hour, "Power Unit #0x66", "Power off/down", true, ""},
		{-9*24*time.Hour + 4*time.Minute, "Power Unit #0x66", "Power off/down", false, ""},
	}
	if m.hotInlet {
		evs = append(evs,
			ev{-14 * time.Minute, "Temperature Inlet_Temp", "Upper Non-critical going high", true, "Reading 41 > Threshold 40 degrees C"},
			ev{-31 * time.Minute, "Temperature Inlet_Temp", "Upper Non-critical going high", false, "Reading 39 < Threshold 40 degrees C"},
			ev{-33 * time.Minute, "Temperature Inlet_Temp", "Upper Non-critical going high", true, "Reading 40 > Threshold 40 degrees C"})
	}
	if m.deadFan {
		evs = append(evs, ev{-2 * time.Hour, "Fan FAN3", "Lower Non-recoverable going low", true, "Reading 0 < Threshold 300 RPM"},
			ev{-2*time.Hour - time.Minute, "Fan FAN3", "Lower Critical going low", true, "Reading 420 < Threshold 500 RPM"})
	}
	if m.psu {
		evs = append(evs, ev{-5 * time.Hour, "Power Supply PSU2_Status", "Power Supply AC lost", true, ""},
			ev{-5 * time.Hour, "Power Supply PSU2_Status", "Redundancy Lost", true, ""})
	}
	if m.selFull {
		evs = append(evs, ev{-40 * time.Minute, "Event Logging Disabled SEL_Status", "Log full", true, ""})
	}
	slices.SortFunc(evs, func(a, b ev) int { return int(b.ago - a.ago) }) // newest first
	res := make([]ipmi.Event, len(evs))
	for i, e := range evs {
		res[i] = ipmi.Event{ID: fmt.Sprintf("%04x", 0x8200+len(evs)-i), Timestamp: t0.Add(e.ago).Format("01/02/2006 15:04:05"),
			Sensor: e.sensor, Event: e.event, Asserted: e.asserted, Detail: e.detail}
	}
	return res
}

func (d *Demo) view(i int, m demoMachine) View {
	snap := d.snapshot(m)
	st := collector.Status(&snap)
	now := metav1.Now()
	st.AgentVersion, st.LastUpdated = "demo", &now
	v := View{
		Name:    m.name,
		Created: metav1.NewTime(d.started.Add(-time.Duration(30+i*7) * 24 * time.Hour)),
		Spec:    bmcv1.BMCSpec{NodeName: m.name, Protocol: bmcv1.ProtocolRedfish, InsecureSkipVerify: true},
		Status:  st,
		Node: &NodeInfo{Ready: snap.Chassis.PowerOn, Roles: []string{m.role}, InternalIP: fmt.Sprintf("10.0.1.%d", 10+i),
			KubeletVersion: "v1.36.2", OSImage: "Ubuntu 24.04.3 LTS", KernelVersion: "6.8.0-79-generic",
			CPU: "128", Memory: "1007 Gi", GPUs: int64(m.gpus), GPUModel: m.gpuModel},
		Agent:         &AgentInfo{Pod: "kube-bmc-agent-" + fmt.Sprintf("%05x", seed(m.name)%0xfffff), IP: fmt.Sprintf("10.244.%d.7", i), Ready: true},
		OOBConfigured: true,
	}
	if m.stale {
		old := metav1.NewTime(time.Now().Add(-47 * time.Minute))
		v.Status.LastUpdated, v.Stale, v.Agent = &old, true, nil
		v.Node.Ready = false
	}
	return v
}
