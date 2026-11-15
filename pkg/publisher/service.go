package publisher

import (
	"context"
	"fmt"
	"time"

	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
)

// ServiceConfig configures a Service. Replicas that share a Coordinator split
// the prpas between them: a prpa is published by the replica that holds its
// lease, so two replicas never publish the same prpa concurrently.
type ServiceConfig struct {
	// Coord holds the dirty marks and leases of the publisher; use
	// coord.NewNATSBuckets with publisher specific buckets so that it does
	// not share state with the checker. coord.Memory serves tests.
	Coord coord.Coordinator
	// Owner identifies this replica.
	Owner string
	// Debounce is the quiet time after the last change of a prpa before it is
	// published; MaxDelay bounds the debouncing of a prpa that changes
	// continuously.
	Debounce, MaxDelay time.Duration
	// Poll, Renew, Workers: see coord.Runner.
	Poll, Renew time.Duration
	Workers     int
	// Now and After replace the clock in tests.
	Now   func() time.Time
	After func(d time.Duration, f func())
	// OnResult and OnError observe finished and failed publishes.
	OnResult func(*Result)
	OnError  func(prpa string, err error)
}

// Service publishes prpas whose build repository changed: changes are marked
// dirty, debounced per prpa, and published under a lease of the prpa.
type Service struct {
	pub    *Publisher
	cfg    ServiceConfig
	runner *coord.Runner
}

// NewService returns a Service around pub.
func NewService(pub *Publisher, cfg ServiceConfig) *Service {
	s := &Service{pub: pub, cfg: cfg}
	s.runner = &coord.Runner{
		Coord: cfg.Coord, Owner: cfg.Owner, Now: cfg.Now, After: cfg.After,
		Debounce: cfg.Debounce, MaxDelay: cfg.MaxDelay, Poll: cfg.Poll, Renew: cfg.Renew,
		Workers: cfg.Workers, OnError: cfg.OnError,
		Process: s.process,
	}
	return s
}

// MarkChanged records that the binaries of prpa changed.
func (s *Service) MarkChanged(ctx context.Context, p buildrepo.Prpa) error {
	if err := s.cfg.Coord.Mark(ctx, p.String()); err != nil {
		return fmt.Errorf("marking %s for publication: %w", p, err)
	}
	return nil
}

// Run publishes until ctx is done.
func (s *Service) Run(ctx context.Context) { s.runner.Run(ctx) }

// Step publishes every ripe prpa once and returns how many this replica
// handled.
func (s *Service) Step(ctx context.Context) (int, error) { return s.runner.Step(ctx) }

func (s *Service) process(ctx context.Context, prpa string) (time.Duration, error) {
	p, err := buildrepo.ParsePrpa(prpa)
	if err != nil {
		return 0, nil // a malformed mark can never succeed; drop it
	}
	res, err := s.pub.Publish(ctx, p)
	if err != nil {
		return 0, err
	}
	if s.cfg.OnResult != nil {
		s.cfg.OnResult(res)
	}
	return 0, nil
}
