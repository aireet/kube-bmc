package ipmitest_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aireet/kube-bmc/ipmi/ipmitest"
)

func TestRunner(t *testing.T) {
	var r ipmitest.Runner
	r.Set("sel elist", []byte("all"))
	r.Set("sel elist last", []byte("newest"))
	run := func(cmd string) (string, error) {
		out, err := r.Run(t.Context(), strings.Fields(cmd)...)
		return string(out), err
	}

	for cmd, want := range map[string]string{
		"sel elist":          "all",
		"sel elist last 30":  "newest", // longest prefix
		"-S cache sel elist": "all",    // the SDR cache option is ignored
	} {
		if got, err := run(cmd); err != nil || got != want {
			t.Errorf("%s: %q, %v", cmd, got, err)
		}
	}
	if _, err := run("sel elistx"); err == nil {
		t.Error("a prefix must end at an argument boundary")
	}

	boom := errors.New("boom")
	r.Fail("sel", boom)
	if _, err := run("sel elist"); !errors.Is(err, boom) {
		t.Errorf("Fail must take precedence over Set: %v", err)
	}
	r.Clear("sel")
	if _, err := run("sel elist"); err != nil {
		t.Errorf("after Clear: %v", err)
	}

	want := []string{"sel elist", "sel elist last 30", "sel elist", "sel elistx", "sel elist", "sel elist"}
	if got := r.Calls(); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Errorf("calls = %q", got)
	}
}

func TestRecordedDumpsSDR(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sdr.cache")
	if _, err := ipmitest.Recorded().Run(t.Context(), "sdr", "dump", file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}
