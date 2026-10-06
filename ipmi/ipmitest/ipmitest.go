// Package ipmitest provides a Runner that answers ipmitool commands with recorded output,
// for testing code that uses package ipmi without a BMC.
package ipmitest

import (
	"context"
	"embed"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Runner answers ipmitool commands with configured output. A command is matched by the
// longest configured prefix of its arguments, so "sel elist" answers
// "sel elist last 100"; the SDR cache option (-S file) is ignored. `sdr dump FILE` writes
// its output to FILE, as ipmitool does. Commands without a match fail.
//
// The zero value answers no command. A Runner is safe for concurrent use.
type Runner struct {
	mu    sync.Mutex
	out   map[string][]byte
	err   map[string]error
	calls []string
}

// Set makes command, such as "sdr elist", print out.
func (r *Runner) Set(command string, out []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.out == nil {
		r.out = map[string][]byte{}
	}
	r.out[command] = out
}

// Fail makes command fail with err. It takes precedence over Set.
func (r *Runner) Fail(command string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = map[string]error{}
	}
	r.err[command] = err
}

// Clear removes the failure set for command.
func (r *Runner) Clear(command string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.err, command)
}

// Calls returns the commands run so far, without the SDR cache option.
func (r *Runner) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// Run implements ipmi.Runner.
func (r *Runner) Run(_ context.Context, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "-S" {
		args = args[2:]
	}
	cmd := strings.Join(args, " ")
	r.mu.Lock()
	r.calls = append(r.calls, cmd)
	key, failed := longestPrefix(r.err, cmd)
	var err error
	var out []byte
	if failed {
		err = r.err[key]
	} else if key, ok := longestPrefix(r.out, cmd); ok {
		out = r.out[key]
	} else {
		err = fmt.Errorf("ipmitest: no output recorded for %q", cmd)
	}
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(args) == 3 && args[0] == "sdr" && args[1] == "dump" {
		return nil, os.WriteFile(args[2], out, 0o600)
	}
	return out, nil
}

func longestPrefix[V any](m map[string]V, cmd string) (string, bool) {
	best, found := "", false
	for k := range m {
		if (cmd == k || strings.HasPrefix(cmd, k+" ")) && len(k) >= len(best) {
			best, found = k, true
		}
	}
	return best, found
}

//go:embed testdata/*.txt
var recorded embed.FS

// recordings maps commands to the files recorded from them.
var recordings = map[string]string{
	"mc info":            "mc_info.txt",
	"mc guid":            "mc_guid.txt",
	"lan print":          "lan_print.txt",
	"fru print":          "fru_print.txt",
	"chassis status":     "chassis_status.txt",
	"dcmi power reading": "dcmi_power_reading.txt",
	"sdr elist":          "sdr_elist.txt",
	"sdr dump":           "sdr_elist.txt",
	"sensor":             "sensor.txt",
	"sel info":           "sel_info.txt",
	"sel elist":          "sel_elist.txt",
	"sel get":            "sel_get.txt",
	"sel clear":          "",
	"chassis power":      "",
	"chassis identify":   "",
}

// Recorded returns a Runner that answers read commands with the output of a real server,
// and accepts the commands that change the BMC state without output. The server is a
// Gooxi SY8108G-G4 with an AMI MegaRAC BMC and eight GPUs, recorded with ipmitool
// 1.8.18; serial numbers and addresses are anonymized. Five of its fans have failed,
// the BMC reports a cooling fault and the System Event Log is full.
func Recorded() *Runner {
	r := &Runner{}
	for cmd, file := range recordings {
		var out []byte
		if file != "" {
			b, err := recorded.ReadFile("testdata/" + file)
			if err != nil {
				panic(err) // embedded at build time
			}
			out = b
		}
		r.Set(cmd, out)
	}
	return r
}
