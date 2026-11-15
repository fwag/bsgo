package coord

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	dirtyBucket = "bsgo-checker-dirty"
	leaseBucket = "bsgo-checker-leases"
	// casRetries bounds the optimistic update loop of Mark.
	casRetries = 8
)

// NATS is a Coordinator backed by two JetStream KV buckets: one for dirty
// marks and one for leases. The lease bucket has a TTL, so an unrenewed lease
// disappears on its own.
type NATS struct {
	dirty  jetstream.KeyValue
	leases jetstream.KeyValue
	now    func() time.Time
}

// NewNATS creates the buckets if needed. leaseTTL is the lifetime of a lease
// that is not renewed.
func NewNATS(ctx context.Context, js jetstream.JetStream, leaseTTL time.Duration) (*NATS, error) {
	dirty, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: dirtyBucket, History: 1})
	if err != nil {
		return nil, fmt.Errorf("creating kv bucket %s: %w", dirtyBucket, err)
	}
	leases, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: leaseBucket, History: 1, TTL: leaseTTL})
	if err != nil {
		return nil, fmt.Errorf("creating kv bucket %s: %w", leaseBucket, err)
	}
	return &NATS{dirty: dirty, leases: leases, now: time.Now}, nil
}

// kvKey encodes an arbitrary string into a valid KV key.
func kvKey(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

type markValue struct {
	Prpa  string `json:"prpa"`
	First int64  `json:"first"`
	Last  int64  `json:"last"`
}

// Mark implements Coordinator.
func (n *NATS) Mark(ctx context.Context, prpa string) error {
	key := kvKey(prpa)
	for range casRetries {
		now := n.now().UnixNano()
		val := markValue{Prpa: prpa, First: now, Last: now}
		entry, err := n.dirty.Get(ctx, key)
		if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) {
			return fmt.Errorf("reading dirty mark of %s: %w", prpa, err)
		}
		if err == nil {
			if jerr := json.Unmarshal(entry.Value(), &val); jerr != nil {
				val = markValue{Prpa: prpa, First: now}
			}
			val.Last = now
		}
		data, err2 := json.Marshal(val)
		if err2 != nil {
			return err2
		}
		if entry == nil {
			_, err = n.dirty.Create(ctx, key, data)
		} else {
			_, err = n.dirty.Update(ctx, key, data, entry.Revision())
		}
		if err == nil {
			return nil
		}
		if !errors.Is(err, jetstream.ErrKeyExists) {
			return fmt.Errorf("marking %s dirty: %w", prpa, err)
		}
	}
	return fmt.Errorf("marking %s dirty: too many concurrent updates", prpa)
}

// Dirty implements Coordinator.
func (n *NATS) Dirty(ctx context.Context) ([]Mark, error) {
	keys, err := n.dirty.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing dirty marks: %w", err)
	}
	var out []Mark
	for _, k := range keys {
		entry, err := n.dirty.Get(ctx, k)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading dirty mark: %w", err)
		}
		var v markValue
		if err := json.Unmarshal(entry.Value(), &v); err != nil || v.Prpa == "" {
			continue
		}
		out = append(out, Mark{Prpa: v.Prpa, First: time.Unix(0, v.First), Last: time.Unix(0, v.Last), Rev: entry.Revision()})
	}
	return out, nil
}

// Clear implements Coordinator.
func (n *NATS) Clear(ctx context.Context, m Mark) error {
	err := n.dirty.Delete(ctx, kvKey(m.Prpa), jetstream.LastRevision(m.Rev))
	if err != nil && !errors.Is(err, jetstream.ErrKeyExists) && !errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("clearing dirty mark of %s: %w", m.Prpa, err)
	}
	return nil
}

// TryLease implements Coordinator.
func (n *NATS) TryLease(ctx context.Context, key, owner string) (Lease, error) {
	k := kvKey(key)
	rev, err := n.leases.Create(ctx, k, []byte(owner))
	if err == nil {
		return &natsLease{kv: n.leases, key: k, owner: owner, rev: rev}, nil
	}
	if !errors.Is(err, jetstream.ErrKeyExists) {
		return nil, fmt.Errorf("taking lease %s: %w", key, err)
	}
	entry, gerr := n.leases.Get(ctx, k)
	if gerr != nil || string(entry.Value()) != owner {
		return nil, ErrHeld
	}
	rev, err = n.leases.Update(ctx, k, []byte(owner), entry.Revision())
	if err != nil {
		return nil, ErrHeld
	}
	return &natsLease{kv: n.leases, key: k, owner: owner, rev: rev}, nil
}

type natsLease struct {
	kv    jetstream.KeyValue
	key   string
	owner string
	rev   uint64
}

func (l *natsLease) Renew(ctx context.Context) error {
	rev, err := l.kv.Update(ctx, l.key, []byte(l.owner), l.rev)
	if err != nil {
		return fmt.Errorf("lease lost: %w", err)
	}
	l.rev = rev
	return nil
}

func (l *natsLease) Release(ctx context.Context) error {
	err := l.kv.Purge(ctx, l.key, jetstream.LastRevision(l.rev))
	if err != nil && !errors.Is(err, jetstream.ErrKeyExists) && !errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("releasing lease: %w", err)
	}
	return nil
}
