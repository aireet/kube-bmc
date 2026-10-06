package collector

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/aireet/kube-bmc/ipmi"
)

var (
	phaseDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kube_bmc_collect_duration_seconds",
		Help:    "Duration of one ipmitool collection phase.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"phase"})
	phaseErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kube_bmc_collect_errors_total",
		Help: "Failed ipmitool collection phases.",
	}, []string{"phase"})
)

func observe(phase string, d time.Duration, err error) {
	phaseDuration.WithLabelValues(phase).Observe(d.Seconds())
	if err != nil {
		phaseErrors.WithLabelValues(phase).Inc()
	}
}

var (
	descUp        = prometheus.NewDesc("kube_bmc_up", "1 if the last collection round reached the BMC.", nil, nil)
	descHealth    = prometheus.NewDesc("kube_bmc_health", "Aggregated health: 0 unknown, 1 ok, 2 warning, 3 critical.", nil, nil)
	descPowerOn   = prometheus.NewDesc("kube_bmc_power_on", "1 if chassis power is on.", nil, nil)
	descWatts     = prometheus.NewDesc("kube_bmc_power_watts", "Instantaneous system power draw (DCMI).", nil, nil)
	descSELUsed   = prometheus.NewDesc("kube_bmc_sel_used_ratio", "Fraction of the System Event Log in use.", nil, nil)
	descSELCount  = prometheus.NewDesc("kube_bmc_sel_entries", "Number of System Event Log entries.", nil, nil)
	descFault     = prometheus.NewDesc("kube_bmc_chassis_fault", "1 for every chassis fault the BMC reports.", []string{"fault"}, nil)
	descSensor    = prometheus.NewDesc("kube_bmc_sensor_value", "Sensor reading in its native unit.", []string{"sensor", "type", "unit"}, nil)
	descSensorSev = prometheus.NewDesc("kube_bmc_sensor_state", "Sensor state: 0 ok, 1 warning, 2 critical, -1 no reading.", []string{"sensor", "type"}, nil)
	descInfo      = prometheus.NewDesc("kube_bmc_info", "BMC inventory as labels.",
		[]string{"manufacturer", "product", "serial", "firmware", "bmc_ip", "bmc_mac"}, nil)
	descInlet    = prometheus.NewDesc("kube_bmc_inlet_temperature_celsius", "Air inlet temperature.", nil, nil)
	descHardware = prometheus.NewDesc("kube_bmc_hardware_info", "Host hardware as labels.",
		[]string{"cpu_model", "cpu_sockets", "cpu_cores", "memory_gib"}, nil)
	descGPUs  = prometheus.NewDesc("kube_bmc_gpus", "Number of GPUs by model.", []string{"model"}, nil)
	descSlots = prometheus.NewDesc("kube_bmc_pcie_slots", "Number of PCIe slots by state (used or free).", []string{"state"}, nil)
)

var sevValue = map[ipmi.Severity]float64{
	ipmi.SeverityOK: 0, ipmi.SeverityWarning: 1, ipmi.SeverityCritical: 2, ipmi.SeverityNoReading: -1,
}

// Metrics exports the latest snapshot. Values are computed at scrape time.
type Metrics struct{ C *Collector }

// NewRegistry returns a registry where every metric carries a constant `node` label,
// so alerts work regardless of how Prometheus discovers the agents.
func NewRegistry(c *Collector, extra ...prometheus.Collector) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	prometheus.WrapRegistererWith(prometheus.Labels{"node": c.snap.Node}, reg).
		MustRegister(append([]prometheus.Collector{phaseDuration, phaseErrors, Metrics{c}}, extra...)...)
	return reg
}

func (m Metrics) Describe(ch chan<- *prometheus.Desc) { prometheus.DescribeByCollect(m, ch) }

func (m Metrics) Collect(ch chan<- prometheus.Metric) {
	s := m.C.Snapshot()
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	up := 0.0
	if _, failed := s.Errors["sensors"]; !failed && len(s.Sensors) > 0 {
		up = 1
	}
	gauge(descUp, up)
	if s.CollectedAt.IsZero() {
		return
	}
	health, _, _ := Evaluate(&s)
	gauge(descHealth, float64(health.Rank()))
	gauge(descPowerOn, b2f(s.Chassis.PowerOn))
	if s.PowerWatts >= 0 {
		gauge(descWatts, float64(s.PowerWatts))
	}
	gauge(descSELUsed, float64(s.SELInfo.UsedPercent)/100)
	gauge(descSELCount, float64(s.SELInfo.Entries))
	for _, f := range s.Chassis.Faults {
		gauge(descFault, 1, f)
	}
	gauge(descInfo, 1, s.FRU.Manufacturer, s.FRU.Product, s.FRU.SerialNumber, s.MC.FirmwareVersion, s.LAN.IPAddress, s.LAN.MACAddress)
	if t, ok := InletTemperature(s.Sensors); ok {
		gauge(descInlet, t)
	}
	if h := s.Hardware; h != nil {
		gauge(descHardware, 1, h.CPU.Model, strconv.Itoa(h.CPU.Sockets), strconv.Itoa(h.CPU.Cores), strconv.Itoa(h.Memory.TotalGiB))
		for model, n := range h.GPUModels() {
			gauge(descGPUs, float64(n), model)
		}
		used := 0
		for _, sl := range h.PCIeSlots {
			if sl.InUse {
				used++
			}
		}
		gauge(descSlots, float64(used), "used")
		gauge(descSlots, float64(len(h.PCIeSlots)-used), "free")
	}
	seen := map[string]bool{}
	for _, sn := range s.Sensors {
		if seen[sn.Name] { // some BMCs report duplicate sensor names
			continue
		}
		seen[sn.Name] = true
		gauge(descSensorSev, sevValue[sn.Severity], sn.Name, string(sn.Type))
		if sn.Value != nil {
			gauge(descSensor, *sn.Value, sn.Name, string(sn.Type), sn.Unit)
		}
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
