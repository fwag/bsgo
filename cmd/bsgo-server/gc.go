package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/gc"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

// startGC runs garbage collection of build repository indexes and superseded
// artifacts every cfg.GCInterval. Replicas share one run lease, so with
// several servers only one collects at a time.
func startGC(ctx context.Context, cfg ServerConfig, js jetstream.JetStream, store storage.Store) {
	if cfg.GCInterval <= 0 {
		logf("GC", "disabled (--gc-interval=0)")
		return
	}
	st, err := s3.NewS3Storage(ctx, cfg.S3Endpoint, "garage", cfg.S3Bucket, "", "")
	if err != nil {
		logf("GC", "disabled: object storage unavailable: %v", err)
		return
	}
	c := &gc.Collector{
		Objects: s3.GCObjects{Store: st}, Store: store, Leases: newCoordinator(ctx, js),
		Owner: "bsgo-server-gc-" + uuid.NewString(),
		Logf: func(format string, args ...any) {
			fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
		},
	}
	opts := gc.Options{Grace: cfg.GCGrace, KeepVersions: cfg.GCKeepIndexVersions}
	go gcLoop(ctx, c, opts, cfg.GCInterval)
}

func gcLoop(ctx context.Context, c *gc.Collector, opts gc.Options, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, err := c.Run(ctx, []string{gc.PrefixBuildRepo, gc.PrefixArtifacts}, opts)
			switch {
			case errors.Is(err, gc.ErrBusy):
				logf("GC", "skipped: another replica is collecting")
			case err != nil && ctx.Err() == nil:
				logf("GC", "run failed: %v", err)
			}
		}
	}
}
