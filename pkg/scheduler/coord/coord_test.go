package coord_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
)

func embeddedJetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	s, err := server.NewServer(&server.Options{Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats did not start")
	}
	t.Cleanup(s.Shutdown)
	nc, js, err := natsqueue.ConnectJetStream(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return js
}

// backends runs a test against the in-memory and the NATS KV Coordinator.
func backends(t *testing.T, ttl time.Duration, run func(t *testing.T, c coord.Coordinator)) {
	t.Run("memory", func(t *testing.T) { run(t, &coord.Memory{TTL: ttl}) })
	t.Run("nats", func(t *testing.T) {
		n, err := coord.NewNATS(context.Background(), embeddedJetStream(t), ttl)
		if err != nil {
			t.Fatal(err)
		}
		run(t, n)
	})
}

func TestLeaseIsExclusivePerKey(t *testing.T) {
	backends(t, time.Minute, func(t *testing.T, c coord.Coordinator) {
		ctx := context.Background()
		l1, err := c.TryLease(ctx, "p/r/a", "replica-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.TryLease(ctx, "p/r/a", "replica-2"); !errors.Is(err, coord.ErrHeld) {
			t.Fatalf("second owner got %v, want ErrHeld", err)
		}
		other, err := c.TryLease(ctx, "p/r/b", "replica-2")
		if err != nil {
			t.Fatalf("another key must be free: %v", err)
		}
		if err := other.Release(ctx); err != nil {
			t.Fatal(err)
		}
		if err := l1.Renew(ctx); err != nil {
			t.Fatalf("renew: %v", err)
		}
		if err := l1.Release(ctx); err != nil {
			t.Fatal(err)
		}
		l2, err := c.TryLease(ctx, "p/r/a", "replica-2")
		if err != nil {
			t.Fatalf("after release: %v", err)
		}
		if err := l1.Renew(ctx); err == nil {
			t.Error("a released lease must not renew")
		}
		_ = l2.Release(ctx)
	})
}

func TestLeaseExpiresWithoutRenewal(t *testing.T) {
	backends(t, 300*time.Millisecond, func(t *testing.T, c coord.Coordinator) {
		ctx := context.Background()
		if _, err := c.TryLease(ctx, "k", "dead-replica"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			l, err := c.TryLease(ctx, "k", "live-replica")
			if err == nil {
				_ = l.Release(ctx)
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("lease of a crashed owner never expired")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func TestMarkClearKeepsRefreshedMarks(t *testing.T) {
	backends(t, time.Minute, func(t *testing.T, c coord.Coordinator) {
		ctx := context.Background()
		if err := c.Mark(ctx, "p/r/a"); err != nil {
			t.Fatal(err)
		}
		first, err := c.Dirty(ctx)
		if err != nil || len(first) != 1 {
			t.Fatalf("dirty %v, %v", first, err)
		}
		if err := c.Mark(ctx, "p/r/a"); err != nil { // refreshed while being processed
			t.Fatal(err)
		}
		if err := c.Clear(ctx, first[0]); err != nil {
			t.Fatal(err)
		}
		again, _ := c.Dirty(ctx)
		if len(again) != 1 {
			t.Fatalf("a mark refreshed after it was read must survive Clear, got %v", again)
		}
		if !again[0].First.Equal(first[0].First) || !again[0].Last.After(first[0].Last) && !again[0].Last.Equal(first[0].Last) {
			t.Errorf("mark times %v -> %v", first[0], again[0])
		}
		if err := c.Clear(ctx, again[0]); err != nil {
			t.Fatal(err)
		}
		if left, _ := c.Dirty(ctx); len(left) != 0 {
			t.Errorf("cleared mark still listed: %v", left)
		}
	})
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestRunnerDebouncesAndSplitsWorkAcrossReplicas(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	shared := &coord.Memory{Now: clock.now}
	var mu sync.Mutex
	handled := map[string][]string{}
	replica := func(owner string) *coord.Runner {
		return &coord.Runner{
			Coord: shared, Owner: owner, Now: clock.now,
			Debounce: 5 * time.Second, MaxDelay: time.Minute,
			Process: func(_ context.Context, prpa string) (time.Duration, error) {
				mu.Lock()
				handled[prpa] = append(handled[prpa], owner)
				mu.Unlock()
				return 0, nil
			},
		}
	}
	r1, r2 := replica("r1"), replica("r2")
	ctx := context.Background()
	for _, p := range []string{"a", "b", "c"} {
		if err := shared.Mark(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := r1.Step(ctx); n != 0 {
		t.Fatalf("processed %d prpas before the debounce time elapsed", n)
	}
	clock.advance(6 * time.Second)
	n1, _ := r1.Step(ctx)
	n2, _ := r2.Step(ctx)
	if n1+n2 != 3 || len(handled) != 3 {
		t.Fatalf("processed %d+%d, handled %v: every prpa exactly once", n1, n2, handled)
	}
	for p, owners := range handled {
		if len(owners) != 1 {
			t.Errorf("prpa %s handled by %v", p, owners)
		}
	}
	if n, _ := r1.Step(ctx); n != 0 {
		t.Error("a processed prpa must not run again")
	}
}

func TestRunnerMaxDelayBoundsDebounce(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	shared := &coord.Memory{Now: clock.now}
	var runs atomic.Int32
	r := &coord.Runner{
		Coord: shared, Owner: "r", Now: clock.now, Debounce: 10 * time.Second, MaxDelay: 25 * time.Second,
		Process: func(context.Context, string) (time.Duration, error) { runs.Add(1); return 0, nil },
	}
	ctx := context.Background()
	for range 6 { // a mark every 5s keeps resetting the debounce
		_ = shared.Mark(ctx, "p")
		clock.advance(5 * time.Second)
		_, _ = r.Step(ctx)
	}
	if runs.Load() != 1 {
		t.Errorf("ran %d times, want once when MaxDelay is reached", runs.Load())
	}
}

func TestRunnerKeepsFailedWorkPendingAndHonorsRetry(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	shared := &coord.Memory{Now: clock.now}
	fail := true
	var after []time.Duration
	r := &coord.Runner{
		Coord: shared, Owner: "r", Now: clock.now, Debounce: time.Second, MaxDelay: time.Minute,
		After: func(d time.Duration, _ func()) { after = append(after, d) },
		Process: func(context.Context, string) (time.Duration, error) {
			if fail {
				return 0, errors.New("boom")
			}
			return 30 * time.Second, nil
		},
	}
	ctx := context.Background()
	_ = shared.Mark(ctx, "p")
	clock.advance(2 * time.Second)
	_, _ = r.Step(ctx)
	if m, _ := shared.Dirty(ctx); len(m) != 1 {
		t.Fatal("a failed prpa must stay dirty")
	}
	fail = false
	clock.advance(2 * time.Second)
	_, _ = r.Step(ctx)
	if m, _ := shared.Dirty(ctx); len(m) != 0 {
		t.Fatal("a processed prpa must be clean")
	}
	if len(after) != 1 || after[0] != 30*time.Second {
		t.Errorf("retry schedule %v, want one 30s retry", after)
	}
}
