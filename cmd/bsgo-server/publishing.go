package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/publisher"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
)

const (
	publisherDirtyBucket = "bsgo-publisher-dirty"
	publisherLeaseBucket = "bsgo-publisher-leases"
	// publisherLeaseTTL is how long a publisher lease survives without
	// renewal, that is how long a crashed replica blocks its prpas.
	publisherLeaseTTL = 30 * time.Second
)

// objectStore returns the S3 store of artifacts and published repositories, or
// a disk stand-in for local runs.
func objectStore(ctx context.Context, cfg ServerConfig) publisher.ObjectStore {
	if st, err := s3.NewS3Storage(ctx, cfg.S3Endpoint, "garage", cfg.S3Bucket, "", ""); err == nil {
		return st
	}
	return NewDiskS3Client("/tmp/bsgo-artifacts")
}

func newPublisherCoordinator(ctx context.Context, js jetstream.JetStream) coord.Coordinator {
	c, err := coord.NewNATSBuckets(ctx, js, publisherLeaseTTL, publisherDirtyBucket, publisherLeaseBucket)
	if err != nil {
		logf("PUBLISHER", "WARNING: NATS KV unavailable (%v); prpa ownership is local to this process", err)
		return &coord.Memory{TTL: publisherLeaseTTL}
	}
	return c
}

// startPublisher publishes the build repository of every prpa whose binaries
// changed. The repo-changed events of the indexer mark prpas dirty; the
// publisher replica that wins the NATS KV lease of a prpa publishes it after
// the debounce time. Replicas keep no local state.
func startPublisher(ctx context.Context, cfg ServerConfig, nc *nats.Conn, js jetstream.JetStream, store storage.Store) {
	objects := objectStore(ctx, cfg)
	if cfg.WorkDir != "" {
		if err := os.MkdirAll(cfg.WorkDir, 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] [PUBLISHER] cannot create %s: %v\n", time.Now().Format("15:04:05"), cfg.WorkDir, err)
			os.Exit(1)
		}
	}
	pub := &publisher.Publisher{
		Store:       objects,
		Index:       buildrepo.NewReader(store, &s3DoDAdapter{storage: objects}),
		Policy:      publisher.ManifestPolicy{Projects: store},
		Signer:      newMetadataSigner(cfg, store),
		DownloadURL: cfg.RepoDownloadURL,
		Events:      &eventPubAdapter{nc: nc},
		TempRoot:    cfg.WorkDir,
		GCGrace:     cfg.PublisherGC,
	}
	svc := publisher.NewService(pub, publisher.ServiceConfig{
		Coord: newPublisherCoordinator(ctx, js), Owner: "bsgo-server-" + uuid.NewString(),
		Debounce: cfg.PublisherDebounce, MaxDelay: 10 * cfg.PublisherDebounce,
		OnResult: logPublished,
		OnError: func(prpa string, err error) {
			fmt.Fprintf(os.Stderr, "[%s] [PUBLISHER] %s: %v\n", time.Now().Format("15:04:05"), prpa, err)
		},
	})
	subscribeRepoChangedForPublisher(ctx, nc, svc)
	go markPublishableAtStartup(ctx, store, svc)
	go svc.Run(ctx)
}

func logPublished(res *publisher.Result) {
	if res.Skipped != "" {
		logf("PUBLISHER", "%s: not published (%s)", res.Prpa, res.Skipped)
		return
	}
	for sr, r := range res.SubRepos {
		logf("PUBLISHER", "%s: published %s repository with %d binaries (%d copied, %d removed)",
			res.Prpa, sr, r.Packages, len(r.Transferred), len(r.Deleted))
	}
}

func subscribeRepoChangedForPublisher(ctx context.Context, nc *nats.Conn, svc *publisher.Service) {
	_, _ = nc.Subscribe(natsqueue.SubjectEventsRepoChanged, func(msg *nats.Msg) {
		p, err := indexer.DecodeEvent(msg.Data)
		if err != nil {
			return
		}
		if err := svc.MarkChanged(ctx, p); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] [PUBLISHER] %v\n", time.Now().Format("15:04:05"), err)
		}
	})
}

// markPublishableAtStartup covers repo-changed events that were missed while
// no publisher was running: every prpa with flags.publish is published once.
func markPublishableAtStartup(ctx context.Context, store storage.Store, svc *publisher.Service) {
	prjs, err := store.ListProjects(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [PUBLISHER] startup resync: %v\n", time.Now().Format("15:04:05"), err)
		return
	}
	for _, prj := range prjs {
		for repo, tgt := range prj.Targets {
			if !tgt.Flags.Publish {
				continue
			}
			for _, arch := range tgt.Architectures {
				p := buildrepo.Prpa{Project: prj.Project.Name, Repo: repo, Arch: arch}
				if err := svc.MarkChanged(ctx, p); err != nil {
					fmt.Fprintf(os.Stderr, "[%s] [PUBLISHER] startup resync: %v\n", time.Now().Format("15:04:05"), err)
				}
			}
		}
	}
}
