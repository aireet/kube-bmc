package ipmi

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// Tool runs the ipmitool binary. The zero value reaches the BMC of the local host
// through the in-band system interface (/dev/ipmi0), which needs no credentials. Setting
// Host reaches a BMC over the network with the IPMI 2.0 lanplus interface.
type Tool struct {
	// Path is the ipmitool binary; empty means "ipmitool" from $PATH.
	Path string
	// Host is the address of a remote BMC, with an optional port (default 623).
	Host     string
	Username string
	// Password is passed to ipmitool in the environment, never on the command line.
	Password string
}

// Run runs ipmitool with args. The error of a failed command includes ipmitool's
// standard error; the output printed before the failure is returned with it.
func (t Tool) Run(ctx context.Context, args ...string) ([]byte, error) {
	path := t.Path
	if path == "" {
		path = "ipmitool"
	}
	cmd := exec.CommandContext(ctx, path, append(t.options(), args...)...)
	if t.Host != "" {
		cmd.Env = append(os.Environ(), "IPMI_PASSWORD="+t.Password)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("ipmitool %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// options returns the interface options for a remote BMC: lanplus, the password from
// $IPMI_PASSWORD (-E), and two retries with a three-second timeout.
func (t Tool) options() []string {
	if t.Host == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(t.Host)
	if err != nil {
		host, port = t.Host, "623"
	}
	return []string{"-I", "lanplus", "-H", host, "-p", port, "-U", t.Username, "-E", "-N", "3", "-R", "2"}
}
