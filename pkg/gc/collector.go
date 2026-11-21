package gc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// LeaseKey is the coordinator lease that serializes GC runs of all server
// replicas and CLI invocations on one bucket.
const LeaseKey = "gc"

// ErrBusy is returned by Run when another GC run holds the lease.
var ErrBusy = errors.New("garbage collection is already running elsewhere")

// Store is what GC needs from the metadata store; storage.Store satisfies it.
type Store interface {
	Pointers
	Projects
}

var _ Store = storage.Store(nil)

// Collector runs GC over the prefixes of one bucket.
type Collector struct {
	Objects Objects
	Store   Store
	// Leases provides the run lease; Owner identifies this process.
	Leases coord.Coordinator
	Owner  string
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// Logf receives one plain line per finished prefix and per error; nil
	// discards them.
	Logf func(format string, args ...any)
	// RenewEvery is the lease renewal interval; zero means 10 seconds.
	RenewEvery time.Duration

	refs *Refs
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Collector) refsCache() *Refs {
	if c.refs == nil {
		c.refs = &Refs{Pointers: c.Store, Objects: c.Objects}
	}
	return c.refs
}

// Plan computes the plans of the given prefixes without deleting anything and
// without taking the lease.
func (c *Collector) Plan(ctx context.Context, prefixes []string, opts Options) ([]Result, error) {
	return c.run(ctx, prefixes, opts, true)
}

// Run takes the GC lease, then plans and deletes per prefix. It returns
// ErrBusy when another run holds the lease. Errors of single prpas are
// recorded in their plan and do not stop the run.
func (c *Collector) Run(ctx context.Context, prefixes []string, opts Options) ([]Result, error) {
	lease, err := c.Leases.TryLease(ctx, LeaseKey, c.Owner)
	if errors.Is(err, coord.ErrHeld) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, fmt.Errorf("gc lease: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := c.keepLease(runCtx, lease, cancel)
	defer func() {
		stop()
		_ = lease.Release(context.WithoutCancel(ctx))
	}()
	return c.run(runCtx, prefixes, opts, false)
}

// keepLease renews the lease until stopped; losing it cancels the run.
func (c *Collector) keepLease(ctx context.Context, lease coord.Lease, cancel context.CancelFunc) (stop func()) {
	every := c.RenewEvery
	if every <= 0 {
		every = 10 * time.Second
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := lease.Renew(ctx); err != nil {
					c.logf("[GC] lease lost, aborting: %v", err)
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func (c *Collector) run(ctx context.Context, prefixes []string, opts Options, dryRun bool) ([]Result, error) {
	var out []Result
	for _, prefix := range prefixes {
		start := time.Now()
		plans, err := c.planPrefix(ctx, prefix, opts)
		if err != nil {
			c.logf("[GC] %s: %v", prefix, err)
			return out, fmt.Errorf("%s: %w", prefix, err)
		}
		plans = Apply(ctx, c.Objects, plans, dryRun, c.protectedFor(prefix))
		res := summarize(prefix, plans, dryRun, time.Since(start))
		c.report(res)
		out = append(out, res)
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	return out, nil
}

func (c *Collector) planPrefix(ctx context.Context, prefix string, opts Options) ([]Plan, error) {
	now := c.now()
	switch prefix {
	case PrefixBuildRepo:
		return PlanIndexGC(ctx, c.Store, c.Objects, now, opts)
	case PrefixArtifacts:
		return PlanArtifactGC(ctx, c.refsCache(), c.Store, now, opts)
	}
	return nil, fmt.Errorf("unknown prefix %q", prefix)
}

func (c *Collector) protectedFor(prefix string) Protected {
	if prefix == PrefixBuildRepo {
		return indexProtected(c.Store)
	}
	return c.refsCache().Keys
}

func (c *Collector) report(r Result) {
	verb, freed := "deleted", "freed"
	if r.DryRun {
		verb, freed = "would delete", "would free"
	}
	c.logf("[GC] %s: kept %d, %s %d objects, %s %s in %ds", r.Prefix, r.Kept, verb, r.Deleted, freed,
		FormatBytes(r.FreedBytes), int(r.Duration.Round(time.Second)/time.Second))
	for _, p := range r.Plans {
		for _, w := range p.Warnings {
			c.logf("[GC] %s %s: %s", r.Prefix, p.Prpa, w)
		}
		if p.Error != "" {
			c.logf("[GC] %s %s: %s", r.Prefix, p.Prpa, p.Error)
		}
	}
}

// FormatBytes renders n in binary units, like "6.6 GiB".
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
