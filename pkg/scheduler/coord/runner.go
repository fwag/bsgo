package coord

import (
	"context"
	"sync"
	"time"
)

// Process handles one dirty prpa. A positive retryAfter asks the runner to
// mark the prpa dirty again after that delay (for example when sources were
// temporarily unavailable).
type Process func(ctx context.Context, prpa string) (retryAfter time.Duration, err error)

// Runner turns dirty marks into Process calls on the replica that wins the
// prpa lease. Any number of runners may use the same Coordinator.
type Runner struct {
	Coord   Coordinator
	Owner   string
	Process Process
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// After runs f after d; nil means time.AfterFunc.
	After func(d time.Duration, f func())
	// Debounce is how long a prpa must stay unmarked before it is processed.
	Debounce time.Duration
	// MaxDelay bounds the debounce: a prpa marked continuously is processed
	// at the latest this long after it first became dirty.
	MaxDelay time.Duration
	// Poll is the scan interval of Run; zero means one second.
	Poll time.Duration
	// Renew is the lease renewal interval; zero means ten seconds.
	Renew time.Duration
	// Workers bounds concurrently processed prpas; zero means 2.
	Workers int
	// OnError receives processing errors; nil ignores them.
	OnError func(prpa string, err error)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) ripe(m Mark) bool {
	now := r.now()
	return now.Sub(m.Last) >= r.Debounce || now.Sub(m.First) >= r.MaxDelay
}

// Run scans until ctx is done.
func (r *Runner) Run(ctx context.Context) {
	poll := r.Poll
	if poll <= 0 {
		poll = time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if _, err := r.Step(ctx); err != nil && r.OnError != nil {
			r.OnError("", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Step processes every ripe mark once and returns how many prpas were
// processed by this replica.
func (r *Runner) Step(ctx context.Context) (int, error) {
	marks, err := r.Coord.Dirty(ctx)
	if err != nil {
		return 0, err
	}
	workers := r.Workers
	if workers <= 0 {
		workers = 2
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
		sem  = make(chan struct{}, workers)
	)
	for _, m := range marks {
		if !r.ripe(m) {
			continue
		}
		lease, err := r.Coord.TryLease(ctx, m.Prpa, r.Owner)
		if err != nil {
			continue // held elsewhere
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.handle(ctx, m, lease)
			mu.Lock()
			done++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return done, nil
}

func (r *Runner) handle(ctx context.Context, m Mark, lease Lease) {
	pctx, cancel := context.WithCancel(ctx)
	stop := r.keepAlive(pctx, cancel, lease)
	retry, err := r.Process(pctx, m.Prpa)
	stop()
	cancel()
	rel, relCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer relCancel()
	_ = lease.Release(rel)
	if err != nil {
		if r.OnError != nil {
			r.OnError(m.Prpa, err)
		}
		// Keep the work pending, but debounced again.
		_ = r.Coord.Mark(rel, m.Prpa)
		return
	}
	_ = r.Coord.Clear(rel, m)
	if retry > 0 {
		r.after(retry, func() { _ = r.Coord.Mark(context.WithoutCancel(ctx), m.Prpa) })
	}
}

func (r *Runner) after(d time.Duration, f func()) {
	if r.After != nil {
		r.After(d, f)
		return
	}
	time.AfterFunc(d, f)
}

// keepAlive renews the lease until stop is called; it cancels the processing
// context when the lease is lost.
func (r *Runner) keepAlive(ctx context.Context, cancel context.CancelFunc, lease Lease) (stop func()) {
	every := r.Renew
	if every <= 0 {
		every = 10 * time.Second
	}
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := lease.Renew(ctx); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-finished
	}
}
