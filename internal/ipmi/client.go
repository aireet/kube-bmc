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

// Client reads the local BMC. It issues read-only commands only.
//
// Reading the SDR repository over KCS takes several seconds, so the client dumps it to a
// cache file and passes `-S` to sensor and SEL commands.
type Client struct {
	Runner   Runner
	CacheDir string
	sdrCache string
}

func NewClient(r Runner, cacheDir string) *Client {
	return &Client{Runner: r, CacheDir: cacheDir}
}

// RefreshSDRCache rebuilds the SDR cache file. On failure, commands read the SDR repository directly.
func (c *Client) RefreshSDRCache(ctx context.Context) error {
	if c.CacheDir == "" {
		return nil
	}
	path := c.CacheDir + "/sdr.cache"
	tmp := path + ".tmp"
	if _, err := c.Runner.Run(ctx, "sdr", "dump", tmp); err != nil {
		c.sdrCache = ""
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	c.sdrCache = path
	return nil
}

func (c *Client) withCache(args ...string) []string {
	if c.sdrCache == "" {
		return args
	}
	return append([]string{"-S", c.sdrCache}, args...)
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
