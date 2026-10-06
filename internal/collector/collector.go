// Package collector polls the local BMC and keeps the latest Snapshot in memory.
//
// Data is collected on three schedules according to its cost and rate of change:
//
//   - Interval: sensors, chassis status, DCMI power and SEL info.
//   - InventoryInterval: FRU, LAN configuration, firmware, sensor thresholds and the host
//     hardware (processors, memory, PCIe slots and GPUs).
//   - SEL entries are read only after `sel info` reports a new entry, at most once per SELMinInterval.
package collector

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aireet/kube-bmc/internal/hostinv"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

// Snapshot is the collected state of a BMC. The agent serves it on /api/v1/snapshot.
type Snapshot struct {
	Node        string                     `json:"node"`
	CollectedAt time.Time                  `json:"collectedAt"`
	MC          ipmi.MCInfo                `json:"mc"`
	GUID        string                     `json:"guid,omitempty"`
	LAN         ipmi.LAN                   `json:"lan"`
	FRU         ipmi.FRU                   `json:"fru"`
	Chassis     ipmi.Chassis               `json:"chassis"`
	SELInfo     ipmi.SELInfo               `json:"selInfo"`
	PowerWatts  int                        `json:"powerWatts"`
	Sensors     []ipmi.Sensor              `json:"sensors"`
	Events      []ipmi.Event               `json:"events"`
	Hardware    *hostinv.Inventory         `json:"hardware,omitempty"`
	Thresholds  map[string]ipmi.Thresholds `json:"-"`
	// Errors holds the most recent error of each failing collection phase.
	Errors map[string]string `json:"errors,omitempty"`
}

type Options struct {
	// Hardware reads the host hardware inventory. Nil disables it.
	Hardware          func(ctx context.Context) (hostinv.Inventory, error)
	Interval          time.Duration
	InventoryInterval time.Duration
	SELMinInterval    time.Duration
	SELEntries        int
	CommandTimeout    time.Duration
	SELTimeout        time.Duration
}

type Collector struct {
	client *ipmi.Client
	opts   Options
	log    *slog.Logger

	mu        sync.RWMutex
	snap      Snapshot
	selReadAt time.Time
	selSeenAt string // SEL "Last Add Time" at the last read
	selBusy   bool
	updates   chan struct{}
	refresh   chan struct{}
	// described caches decoded descriptions of SEL entries. Entries are immutable, so the
	// key only has to distinguish reused record IDs after the log was cleared.
	described map[string]string
}

// maxSELLookups bounds the `sel get` calls per SEL read.
const maxSELLookups = 25

func New(node string, client *ipmi.Client, opts Options, log *slog.Logger) *Collector {
	return &Collector{
		client:    client,
		opts:      opts,
		log:       log,
		snap:      Snapshot{Node: node, PowerWatts: -1, Errors: map[string]string{}},
		updates:   make(chan struct{}, 1),
		refresh:   make(chan struct{}, 1),
		described: map[string]string{},
	}
}

// Updates receives a value after each collection round. Values are coalesced.
func (c *Collector) Updates() <-chan struct{} { return c.updates }

// Snapshot returns a copy of the latest state with thresholds merged into the sensors.
func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.snap
	s.Sensors = make([]ipmi.Sensor, len(c.snap.Sensors))
	for i, sn := range c.snap.Sensors {
		if t, ok := c.snap.Thresholds[sn.Name]; ok {
			sn.Thresholds = &t
		}
		s.Sensors[i] = sn
	}
	s.Events = slices.Clone(c.snap.Events)
	s.Errors = make(map[string]string, len(c.snap.Errors))
	for k, v := range c.snap.Errors {
		s.Errors[k] = v
	}
	return s
}

// Refresh requests an immediate collection round that also re-reads the SEL, for example
// after the log was cleared.
func (c *Collector) Refresh() {
	select {
	case c.refresh <- struct{}{}:
	default:
	}
}

// Fresh reports whether a collection round completed within maxAge.
func (c *Collector) Fresh(maxAge time.Duration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.snap.CollectedAt.IsZero() && time.Since(c.snap.CollectedAt) <= maxAge
}

// Ready reports whether at least one full round has completed.
func (c *Collector) Ready() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.snap.CollectedAt.IsZero()
}

func (c *Collector) Run(ctx context.Context) {
	c.step(ctx, "sdr-cache", c.opts.SELTimeout, func(ctx context.Context) error { return c.client.RefreshSDRCache(ctx) })
	c.inventory(ctx)
	c.fast(ctx)

	fast := time.NewTicker(c.opts.Interval)
	inv := time.NewTicker(c.opts.InventoryInterval)
	defer fast.Stop()
	defer inv.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			c.fast(ctx)
		case <-c.refresh:
			c.mu.Lock()
			c.snap.Events, c.selReadAt, c.selSeenAt = nil, time.Time{}, ""
			c.mu.Unlock()
			c.fast(ctx)
		case <-inv.C:
			c.step(ctx, "sdr-cache", c.opts.SELTimeout, func(ctx context.Context) error { return c.client.RefreshSDRCache(ctx) })
			c.inventory(ctx)
		}
	}
}

// step runs one phase with its own timeout and records the error, if any.
func (c *Collector) step(ctx context.Context, phase string, timeout time.Duration, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	err := fn(ctx)
	observe(phase, time.Since(start), err)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.snap.Errors[phase] != err.Error() {
			c.log.Warn("collection failed", "phase", phase, "err", err)
		}
		c.snap.Errors[phase] = err.Error()
	} else {
		delete(c.snap.Errors, phase)
	}
}

func (c *Collector) inventory(ctx context.Context) {
	t := c.opts.CommandTimeout
	c.step(ctx, "mc", t, func(ctx context.Context) error {
		mc, err := c.client.MCInfo(ctx)
		guid, _ := c.client.GUID(ctx)
		c.set(func(s *Snapshot) { s.MC, s.GUID = mc, guid })
		return err
	})
	c.step(ctx, "lan", t, func(ctx context.Context) error {
		lan, err := c.client.LAN(ctx)
		if err == nil {
			c.set(func(s *Snapshot) { s.LAN = lan })
		}
		return err
	})
	c.step(ctx, "fru", t, func(ctx context.Context) error {
		fru, err := c.client.FRU(ctx)
		c.set(func(s *Snapshot) { s.FRU = fru })
		return err
	})
	if c.opts.Hardware != nil {
		c.step(ctx, "hardware", t, func(ctx context.Context) error {
			inv, err := c.opts.Hardware(ctx)
			if inv.CPU.Sockets > 0 || len(inv.PCIeSlots) > 0 || len(inv.GPUs) > 0 {
				c.set(func(s *Snapshot) { s.Hardware = &inv })
			}
			return err
		})
	}
	c.step(ctx, "thresholds", c.opts.SELTimeout, func(ctx context.Context) error {
		th, err := c.client.Thresholds(ctx)
		if err == nil {
			c.set(func(s *Snapshot) { s.Thresholds = th })
		}
		return err
	})
}

func (c *Collector) fast(ctx context.Context) {
	t := c.opts.CommandTimeout
	c.step(ctx, "chassis", t, func(ctx context.Context) error {
		ch, err := c.client.Chassis(ctx)
		if err == nil {
			c.set(func(s *Snapshot) { s.Chassis = ch })
		}
		return err
	})
	c.step(ctx, "power", t, func(ctx context.Context) error {
		w, err := c.client.Power(ctx)
		c.set(func(s *Snapshot) { s.PowerWatts = w })
		return err
	})
	c.step(ctx, "sensors", t, func(ctx context.Context) error {
		sn, err := c.client.Sensors(ctx)
		if err == nil {
			c.set(func(s *Snapshot) { s.Sensors = sn })
		}
		return err
	})
	c.step(ctx, "sel-info", t, func(ctx context.Context) error {
		info, err := c.client.SELInfo(ctx)
		if err == nil {
			c.set(func(s *Snapshot) { s.SELInfo = info })
		}
		return err
	})
	c.set(func(s *Snapshot) { s.CollectedAt = time.Now() })
	c.maybeReadSEL(ctx)

	select {
	case c.updates <- struct{}{}:
	default:
	}
}

// maybeReadSEL starts a background SEL read if the log changed and SELMinInterval has elapsed.
func (c *Collector) maybeReadSEL(ctx context.Context) {
	c.mu.Lock()
	last := c.snap.SELInfo.LastAddTime
	due := !c.selBusy && last != c.selSeenAt && time.Since(c.selReadAt) >= c.opts.SELMinInterval
	if due {
		c.selBusy = true
	}
	c.mu.Unlock()
	if !due {
		return
	}
	go func() {
		c.step(ctx, "sel", c.opts.SELTimeout, func(ctx context.Context) error {
			ev, err := c.client.SEL(ctx, c.opts.SELEntries)
			if err == nil {
				slices.Reverse(ev) // newest first
				c.describe(ctx, ev)
				c.set(func(s *Snapshot) { s.Events = ev })
			}
			return err
		})
		c.mu.Lock()
		c.selBusy, c.selReadAt, c.selSeenAt = false, time.Now(), last
		c.mu.Unlock()
	}()
}

// describe fills in descriptions that ipmitool left empty by decoding the raw event data.
func (c *Collector) describe(ctx context.Context, events []ipmi.Event) {
	lookups := 0
	for i := range events {
		e := &events[i]
		if e.Event != "" {
			continue
		}
		key := e.ID + "|" + e.Timestamp + "|" + e.Sensor
		desc, ok := c.described[key]
		if !ok {
			if lookups == maxSELLookups {
				continue
			}
			lookups++
			if r, err := c.client.SELRecord(ctx, e.ID); err == nil {
				desc = r.Describe()
			}
			if len(c.described) > 4*c.opts.SELEntries {
				clear(c.described)
			}
			c.described[key] = desc
		}
		e.Event = desc
	}
}

func (c *Collector) set(fn func(*Snapshot)) {
	c.mu.Lock()
	fn(&c.snap)
	c.mu.Unlock()
}
