package ipmi

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

// dumpRunner creates the file requested by `sdr dump` and records whether other commands
// were given the cache file.
type dumpRunner struct {
	failDump atomic.Bool
	cached   atomic.Int64
	direct   atomic.Int64
}

func (r *dumpRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if slices.Equal(args[:2], []string{"sdr", "dump"}) {
		if r.failDump.Load() {
			return nil, errors.New("sdr dump failed")
		}
		return nil, os.WriteFile(args[2], []byte("sdr"), 0o600)
	}
	if args[0] == "-S" {
		r.cached.Add(1)
	} else {
		r.direct.Add(1)
	}
	return nil, nil
}

func TestSDRCache(t *testing.T) {
	r := &dumpRunner{}
	c := NewClient(r, t.TempDir())
	if _, err := c.SELText(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.direct.Load() != 1 {
		t.Fatal("commands must read the SDR repository directly before the cache exists")
	}
	if err := c.RefreshSDRCache(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SELText(t.Context()); err != nil || r.cached.Load() != 1 {
		t.Fatalf("commands must use the cache once it exists (err %v)", err)
	}
	r.failDump.Store(true)
	if err := c.RefreshSDRCache(t.Context()); err == nil {
		t.Fatal("expected the failed dump to be reported")
	}
	if _, _ = c.SELText(t.Context()); r.direct.Load() != 2 {
		t.Fatal("a failed refresh must stop the use of the cache")
	}
}

// The collector refreshes the cache while the action controller reads the SEL. Run with
// -race.
func TestSDRCacheConcurrentUse(t *testing.T) {
	c := NewClient(&dumpRunner{}, t.TempDir())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				_ = c.RefreshSDRCache(t.Context())
			}
		})
		wg.Go(func() {
			for range 50 {
				_, _ = c.SELText(t.Context())
				_, _ = c.Sensors(t.Context())
			}
		})
	}
	wg.Wait()
}
