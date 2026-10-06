package ipmi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// A Runner runs ipmitool with args and returns its standard output. Tool runs the
// ipmitool binary; package ipmitest provides a Runner that replays recorded output.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Client reads and controls a BMC. Methods that change the BMC state are SetPower,
// Identify and ClearSEL; all others only read.
//
// A Client is safe for concurrent use.
type Client struct {
	r Runner

	cacheMu sync.Mutex             // serializes CacheSDR
	sdr     atomic.Pointer[string] // path of a valid SDR cache file, if any
}

// New returns a Client that sends commands through r.
func New(r Runner) *Client { return &Client{r: r} }

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	return c.r.Run(ctx, args...)
}

// cached prefixes args with the SDR cache option once CacheSDR has succeeded.
func (c *Client) cached(args ...string) []string {
	if p := c.sdr.Load(); p != nil {
		return append([]string{"-S", *p}, args...)
	}
	return args
}

// CacheSDR dumps the Sensor Data Repository to a file in dir. Sensor and event commands
// then read the file instead of the repository, which takes several seconds over the
// in-band interface. Call it again to pick up changes, for example after a firmware
// update. If it fails, commands read the repository directly.
func (c *Client) CacheSDR(ctx context.Context, dir string) error {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	path := dir + "/sdr.cache"
	tmp := path + ".tmp"
	if _, err := c.run(ctx, "sdr", "dump", tmp); err != nil {
		c.sdr.Store(nil)
		return err
	}
	// The rename is atomic: concurrent commands read either the old or the new file.
	if err := os.Rename(tmp, path); err != nil {
		c.sdr.Store(nil)
		return err
	}
	c.sdr.Store(&path)
	return nil
}

// Controller returns the device information of the BMC.
func (c *Client) Controller(ctx context.Context) (Controller, error) {
	out, err := c.run(ctx, "mc", "info")
	if err != nil {
		return Controller{}, err
	}
	return parseController(out), nil
}

// GUID returns the system GUID. Not every BMC implements it.
func (c *Client) GUID(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "mc", "guid")
	if err != nil {
		return "", err
	}
	return parseGUID(out), nil
}

// lanChannels are tried in order: most vendors use channel 1, Dell 8, some Supermicro
// boards 2.
var lanChannels = []int{1, 2, 8, 3}

// LAN returns the configuration of the first LAN channel that has an IP address.
func (c *Client) LAN(ctx context.Context) (LAN, error) {
	var errs []error
	for _, ch := range lanChannels {
		out, err := c.run(ctx, "lan", "print", strconv.Itoa(ch))
		if err != nil {
			errs = append(errs, err)
		}
		// Supermicro BMCs print the configuration and then exit 1 on an unsupported
		// trailing parameter, so output is parsed even after an error.
		lan := parseLAN(out)
		if lan.IPAddress == "" || lan.IPAddress == "0.0.0.0" {
			continue
		}
		lan.Channel = ch
		if lan.Gateway == "" {
			// Get LAN Configuration Parameters, parameter 12: default gateway address.
			if raw, err := c.run(ctx, "raw", "0x0c", "0x02", strconv.Itoa(ch), "0x0c", "0x00", "0x00"); err == nil {
				lan.Gateway, _ = parseRawIPv4(raw)
			}
		}
		return lan, nil
	}
	return LAN{}, errors.Join(append(errs, errors.New("no LAN channel has an IP address"))...)
}

// FRU returns the inventory of FRU device 0, the system board.
func (c *Client) FRU(ctx context.Context) (FRU, error) {
	out, err := c.run(ctx, "fru", "print", "0")
	// Many BMCs exit non-zero on one unreadable FRU area while printing the others.
	fru := parseFRU(out)
	if err != nil && fru == (FRU{}) {
		return FRU{}, err
	}
	return fru, nil
}

// Chassis returns the chassis power and fault state.
func (c *Client) Chassis(ctx context.Context) (Chassis, error) {
	out, err := c.run(ctx, "chassis", "status")
	if err != nil {
		return Chassis{}, err
	}
	return parseChassis(out), nil
}

// PowerReading returns the instantaneous power draw in watts measured by DCMI. BMCs
// without DCMI power measurement return an error wrapping errors.ErrUnsupported.
func (c *Client) PowerReading(ctx context.Context) (int, error) {
	out, err := c.run(ctx, "dcmi", "power", "reading")
	if err != nil {
		return 0, err
	}
	w, ok := parsePowerReading(out)
	if !ok {
		return 0, fmt.Errorf("%w: no DCMI power reading", errors.ErrUnsupported)
	}
	return w, nil
}

// Sensors returns the sensors of the Sensor Data Repository with their current readings.
func (c *Client) Sensors(ctx context.Context) ([]Sensor, error) {
	out, err := c.run(ctx, c.cached("sdr", "elist")...)
	s := parseSDR(out)
	if err != nil && len(s) == 0 {
		return nil, err
	}
	return s, nil
}

// Thresholds returns the thresholds of the analog sensors by sensor name. Reading them
// is slow, so callers typically refresh them less often than the readings.
func (c *Client) Thresholds(ctx context.Context) (map[string]Thresholds, error) {
	out, err := c.run(ctx, c.cached("sensor")...)
	th := parseThresholds(out)
	if err != nil && len(th) == 0 {
		return nil, err
	}
	return th, nil
}

// SELInfo returns the state of the System Event Log.
func (c *Client) SELInfo(ctx context.Context) (SELInfo, error) {
	out, err := c.run(ctx, "sel", "info")
	if err != nil {
		return SELInfo{}, err
	}
	return parseSELInfo(out), nil
}

// Events returns the newest n entries of the System Event Log, oldest first. ipmitool
// reads the whole log, which can take tens of seconds.
func (c *Client) Events(ctx context.Context, n int) ([]Event, error) {
	out, err := c.run(ctx, c.cached("sel", "elist", "last", strconv.Itoa(n))...)
	if err != nil {
		return nil, err
	}
	return parseEvents(out), nil
}

// EventRecord returns the raw content of the System Event Log entry id, as listed in
// Event.ID.
func (c *Client) EventRecord(ctx context.Context, id string) (EventRecord, error) {
	out, err := c.run(ctx, c.cached("sel", "get", "0x"+id)...)
	if err != nil {
		return EventRecord{}, err
	}
	return parseEventRecord(out)
}

// ExportSEL returns the complete System Event Log as printed by `ipmitool sel elist`,
// for archiving before ClearSEL.
func (c *Client) ExportSEL(ctx context.Context) ([]byte, error) {
	return c.run(ctx, c.cached("sel", "elist")...)
}

// ClearSEL erases the System Event Log.
func (c *Client) ClearSEL(ctx context.Context) error {
	_, err := c.run(ctx, "sel", "clear")
	return err
}

// PowerAction is a chassis power control operation.
type PowerAction string

const (
	PowerOn    PowerAction = "on"
	PowerOff   PowerAction = "off"   // immediate, without shutting down the OS
	PowerCycle PowerAction = "cycle" // off, then on after a BMC-defined interval
	PowerReset PowerAction = "reset" // hard reset
	// PowerSoftOff asks the operating system to shut down through ACPI.
	PowerSoftOff PowerAction = "soft"
)

// SetPower performs a chassis power control operation.
func (c *Client) SetPower(ctx context.Context, a PowerAction) error {
	switch a {
	case PowerOn, PowerOff, PowerCycle, PowerReset, PowerSoftOff:
	default:
		return fmt.Errorf("unknown power action %q", a)
	}
	_, err := c.run(ctx, "chassis", "power", string(a))
	return err
}

const (
	// IdentifyIndefinitely keeps the identify light on until it is turned off.
	IdentifyIndefinitely time.Duration = -1
	// MaxIdentify is the longest timed identify interval IPMI supports.
	MaxIdentify = 255 * time.Second
)

// Identify turns the chassis identify light on for d, or off if d is zero. d is
// truncated to seconds and limited to MaxIdentify; IdentifyIndefinitely keeps the light
// on until it is turned off, which some BMCs do not support.
func (c *Client) Identify(ctx context.Context, d time.Duration) error {
	arg := "force"
	if d >= 0 {
		arg = strconv.Itoa(int(min(d, MaxIdentify) / time.Second))
	}
	_, err := c.run(ctx, "chassis", "identify", arg)
	return err
}
