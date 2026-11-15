package coord

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Memory is an in-memory Coordinator. Several Memory values never share
// state; use one instance for all simulated replicas of a test.
type Memory struct {
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// TTL is the lease lifetime; zero means 30 seconds.
	TTL time.Duration

	mu     sync.Mutex
	rev    uint64
	marks  map[string]Mark
	leases map[string]memLease
}

type memLease struct {
	owner   string
	expires time.Time
}

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Memory) ttl() time.Duration {
	if m.TTL > 0 {
		return m.TTL
	}
	return 30 * time.Second
}

// Mark implements Coordinator.
func (m *Memory) Mark(_ context.Context, prpa string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.marks == nil {
		m.marks = map[string]Mark{}
	}
	now := m.now()
	m.rev++
	cur, ok := m.marks[prpa]
	if !ok {
		cur = Mark{Prpa: prpa, First: now}
	}
	cur.Last, cur.Rev = now, m.rev
	m.marks[prpa] = cur
	return nil
}

// Dirty implements Coordinator.
func (m *Memory) Dirty(_ context.Context) ([]Mark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Mark, 0, len(m.marks))
	for _, mk := range m.marks {
		out = append(out, mk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prpa < out[j].Prpa })
	return out, nil
}

// Clear implements Coordinator.
func (m *Memory) Clear(_ context.Context, mk Mark) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.marks[mk.Prpa]; ok && cur.Rev == mk.Rev {
		delete(m.marks, mk.Prpa)
	}
	return nil
}

// TryLease implements Coordinator.
func (m *Memory) TryLease(_ context.Context, key, owner string) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leases == nil {
		m.leases = map[string]memLease{}
	}
	cur, ok := m.leases[key]
	if ok && cur.owner != owner && m.now().Before(cur.expires) {
		return nil, ErrHeld
	}
	m.leases[key] = memLease{owner: owner, expires: m.now().Add(m.ttl())}
	return &memHandle{m: m, key: key, owner: owner}, nil
}

type memHandle struct {
	m          *Memory
	key, owner string
}

func (h *memHandle) Renew(_ context.Context) error {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	cur, ok := h.m.leases[h.key]
	if !ok || cur.owner != h.owner {
		return fmt.Errorf("lease %s lost", h.key)
	}
	h.m.leases[h.key] = memLease{owner: h.owner, expires: h.m.now().Add(h.m.ttl())}
	return nil
}

func (h *memHandle) Release(_ context.Context) error {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if cur, ok := h.m.leases[h.key]; ok && cur.owner == h.owner {
		delete(h.m.leases, h.key)
	}
	return nil
}
