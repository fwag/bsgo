package main

import (
	"context"

	"github.com/google/uuid"
	"github.com/openSUSE/bsgo/pkg/scheduler/vrev"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// startVrev runs the resolver of package revision release numbers. Replicas
// share the work through row leases in the store, so every control plane
// instance may run workers.
func startVrev(ctx context.Context, cfg ServerConfig, store storage.Store) {
	if cfg.VrevWorkers <= 0 {
		logf("VREV", "disabled (--vrev-workers=0): units whose revision is not resolved wait")
		return
	}
	r := vrev.New(store, vrev.Options{
		Owner:   "bsgo-server-vrev-" + uuid.NewString(),
		Workers: cfg.VrevWorkers,
		Timeout: cfg.VrevTimeout,
		Delay:   cfg.VrevDelay,
		Logf:    func(format string, args ...any) { logf("VREV", format, args...) },
	})
	logf("VREV", "resolver started: %d workers, %s pause after each resolution, %s timeout",
		cfg.VrevWorkers, cfg.VrevDelay, cfg.VrevTimeout)
	go r.Run(ctx)
}
