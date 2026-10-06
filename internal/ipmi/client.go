package ipmi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Runner executes ipmitool commands.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Exec runs the ipmitool binary. Extra is prepended to the arguments of every call.
type Exec struct {
	Path  string
	Extra []string
	// Env is appended to the process environment, e.g. IPMI_PASSWORD for `-E`.
	Env []string
}

func (e Exec) Run(ctx context.Context, args ...string) ([]byte, error) {
	path := e.Path
	if path == "" {
		path = "ipmitool"
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, append(append([]string{}, e.Extra...), args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if len(e.Env) > 0 {
		cmd.Env = append(os.Environ(), e.Env...)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("ipmitool %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// Client talks to the local BMC. Collection uses read-only commands; ChassisPower,
// Identify and ClearSEL change the BMC state and are only used to execute BMCActions.
//
// Reading the SDR repository over KCS takes several seconds, so the client dumps it to a
// cache file and passes `-S` to sensor and SEL commands.
//
// A Client is safe for concurrent use: the collector and the action controller share one.
type Client struct {
	Runner   Runner
	CacheDir string

	// refreshMu serializes RefreshSDRCache, which writes a fixed temporary file.
	refreshMu sync.Mutex
	// sdrCache is the path of a valid cache file, or nil when commands must read the
	// SDR repository directly.
	sdrCache atomic.Pointer[string]
}

func NewClient(r Runner, cacheDir string) *Client {
	return &Client{Runner: r, CacheDir: cacheDir}
}

// RefreshSDRCache rebuilds the SDR cache file. On failure, commands read the SDR repository directly.
func (c *Client) RefreshSDRCache(ctx context.Context) error {
	if c.CacheDir == "" {
		return nil
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	path := c.CacheDir + "/sdr.cache"
	tmp := path + ".tmp"
	if _, err := c.Runner.Run(ctx, "sdr", "dump", tmp); err != nil {
		c.sdrCache.Store(nil)
		return err
	}
	// The rename is atomic, so concurrent commands read either the old or the new file.
	if err := os.Rename(tmp, path); err != nil {
		c.sdrCache.Store(nil)
		return err
	}
	c.sdrCache.Store(&path)
	return nil
}

func (c *Client) withCache(args ...string) []string {
	path := c.sdrCache.Load()
	if path == nil {
		return args
	}
	return append([]string{"-S", *path}, args...)
}

func (c *Client) MCInfo(ctx context.Context) (MCInfo, error) {
	out, err := c.Runner.Run(ctx, "mc", "info")
	return ParseMCInfo(out), err
}

func (c *Client) GUID(ctx context.Context) (string, error) {
	out, err := c.Runner.Run(ctx, "mc", "guid")
	return ParseGUID(out), err
}

// LAN finds the first LAN channel with a configured address. Vendors use different
// channel numbers (1 on most, 8 on Dell, 2 on some Supermicro boards).
func (c *Client) LAN(ctx context.Context) (LAN, error) {
	var errs []error
	for _, ch := range []int{1, 2, 8, 3} {
		out, err := c.Runner.Run(ctx, "lan", "print", strconv.Itoa(ch))
		// Supermicro BMCs print the configuration, then exit 1 on an unsupported trailing
		// parameter, so partial output is still used.
		lan := ParseLAN(out)
		if err != nil {
			errs = append(errs, err)
		}
		if lan.IPAddress != "" && lan.IPAddress != "0.0.0.0" {
			lan.Channel = ch
			if lan.Gateway == "" {
				// Parameter 12: default gateway address. Read directly when `lan print`
				// stopped before printing it.
				raw, err := c.Runner.Run(ctx, "raw", "0x0c", "0x02", strconv.Itoa(ch), "0x0c", "0x00", "0x00")
				if err == nil {
					lan.Gateway, _ = parseRawIPv4(raw)
				}
			}
			return lan, nil
		}
	}
	return LAN{}, errors.Join(append(errs, errors.New("no LAN channel with an IP address"))...)
}

func (c *Client) FRU(ctx context.Context) (FRU, error) {
	out, err := c.Runner.Run(ctx, "fru", "print", "0")
	// Many BMCs exit non-zero on one unreadable FRU area while printing the rest.
	fru := ParseFRU(out)
	if err != nil && fru == (FRU{}) {
		return fru, err
	}
	return fru, nil
}

func (c *Client) Chassis(ctx context.Context) (Chassis, error) {
	out, err := c.Runner.Run(ctx, "chassis", "status")
	return ParseChassis(out), err
}

func (c *Client) SELInfo(ctx context.Context) (SELInfo, error) {
	out, err := c.Runner.Run(ctx, "sel", "info")
	return ParseSELInfo(out), err
}

// SEL returns the newest n events. ipmitool reads the entire log, which can take tens of seconds.
func (c *Client) SEL(ctx context.Context, n int) ([]Event, error) {
	out, err := c.Runner.Run(ctx, c.withCache("sel", "elist", "last", strconv.Itoa(n))...)
	return ParseSEL(out), err
}

// SELText returns the complete System Event Log as printed by `ipmitool sel elist`.
func (c *Client) SELText(ctx context.Context) ([]byte, error) {
	return c.Runner.Run(ctx, c.withCache("sel", "elist")...)
}

// ClearSEL erases the System Event Log.
func (c *Client) ClearSEL(ctx context.Context) error {
	_, err := c.Runner.Run(ctx, "sel", "clear")
	return err
}

// Identify turns the chassis identify light on until it is turned off, or off. BMCs
// without support for indefinite identify keep it on for the maximum interval of 255
// seconds instead; the returned note says so.
func (c *Client) Identify(ctx context.Context, on bool) (note string, err error) {
	if !on {
		_, err = c.Runner.Run(ctx, "chassis", "identify", "0")
		return "", err
	}
	if _, err = c.Runner.Run(ctx, "chassis", "identify", "force"); err == nil {
		return "", nil
	}
	if _, err2 := c.Runner.Run(ctx, "chassis", "identify", "255"); err2 != nil {
		return "", errors.Join(err, err2)
	}
	return "the BMC does not support an indefinite identify light; it stays on for 255 seconds", nil
}

// ChassisPower runs `ipmitool chassis power <verb>`.
func (c *Client) ChassisPower(ctx context.Context, verb string) error {
	_, err := c.Runner.Run(ctx, "chassis", "power", verb)
	return err
}

// SELRecord returns the raw content of one SEL entry.
func (c *Client) SELRecord(ctx context.Context, id string) (SELRecord, error) {
	out, err := c.Runner.Run(ctx, c.withCache("sel", "get", "0x"+id)...)
	if err != nil {
		return SELRecord{}, err
	}
	return ParseSELRecord(out)
}

// Power returns the DCMI instantaneous power reading, or -1 when the BMC has no DCMI support.
func (c *Client) Power(ctx context.Context) (int, error) {
	out, err := c.Runner.Run(ctx, "dcmi", "power", "reading")
	if err != nil {
		return -1, err
	}
	return ParseDCMIPower(out), nil
}

func (c *Client) Sensors(ctx context.Context) ([]Sensor, error) {
	out, err := c.Runner.Run(ctx, c.withCache("sdr", "elist")...)
	s := ParseSDR(out)
	if err != nil && len(s) == 0 {
		return nil, err
	}
	return s, nil
}

func (c *Client) Thresholds(ctx context.Context) (map[string]Thresholds, error) {
	out, err := c.Runner.Run(ctx, c.withCache("sensor")...)
	th := ParseThresholds(out)
	if err != nil && len(th) == 0 {
		return nil, err
	}
	return th, nil
}
