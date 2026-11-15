// Package coord partitions scheduler work by prpa (project, repository,
// architecture) across any number of server replicas. State changes mark a
// prpa dirty; a replica that wants to process a dirty prpa first takes its
// lease. Both live in NATS KV (or in memory for tests), so replicas share no
// filesystem and no process state.
package coord

import (
	"context"
	"errors"
	"time"
)

// ErrHeld is returned by TryLease when another owner holds the lease.
var ErrHeld = errors.New("lease held by another owner")

// Mark is the dirty state of one prpa. First is when it became dirty, Last
// when it was marked most recently; Rev identifies the stored version so that
// Clear never drops a mark that was refreshed after it was read.
type Mark struct {
	Prpa  string
	First time.Time
	Last  time.Time
	Rev   uint64
}

// Lease is exclusive ownership of one key. It expires on its own unless it is
// renewed, so a crashed owner never blocks a prpa for long.
type Lease interface {
	// Renew extends the lease. It fails when the lease was lost.
	Renew(ctx context.Context) error
	// Release gives the lease up.
	Release(ctx context.Context) error
}

// Coordinator is the shared state of the scheduler replicas.
type Coordinator interface {
	// Mark records that prpa must be checked again.
	Mark(ctx context.Context, prpa string) error
	// Dirty lists the pending marks.
	Dirty(ctx context.Context) ([]Mark, error)
	// Clear removes a mark unless it was refreshed since it was read.
	Clear(ctx context.Context, m Mark) error
	// TryLease takes the lease of key for owner, or fails with ErrHeld. The
	// same owner re-taking its own lease succeeds.
	TryLease(ctx context.Context, key, owner string) (Lease, error)
}
