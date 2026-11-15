package natsqueue

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	StreamTriggers    = "TRIGGERS"
	StreamJobsPending = "JOBS_PENDING"
	StreamJobStatus   = "JOB_STATUS"
	StreamPublish     = "PUBLISH"

	SubjectTriggersPattern = "bsgo.triggers.>"
	SubjectJobsPending     = "bsgo.jobs.pending"
	SubjectJobStatus       = "bsgo.status"
	SubjectPublishPattern  = "bsgo.publish.>"
	SubjectEventsPublished = "bsgo.events.published"
	// SubjectEventsRepoChanged announces new binaries in a build repository.
	SubjectEventsRepoChanged = "bsgo.events.repo.changed"

	PriorityHigh = "high"
	PriorityMed  = "med"
	PriorityLow  = "low"
)

// TriggerSubject returns the prioritized NATS subject for a trigger.
// e.g. bsgo.triggers.high.openSUSE:Factory.curl
func TriggerSubject(priority, project, pkg string) string {
	p := priority
	if p != PriorityLow && p != PriorityMed {
		p = PriorityHigh
	}
	return fmt.Sprintf("bsgo.triggers.%s.%s.%s", p, project, pkg)
}

func SetupStreams(ctx context.Context, js jetstream.JetStream) error {
	configs := []jetstream.StreamConfig{
		{
			Name:      StreamTriggers,
			Subjects:  []string{SubjectTriggersPattern},
			Retention: jetstream.LimitsPolicy,
			MaxAge:    7 * 24 * time.Hour,
			Storage:   jetstream.FileStorage,
		},
		{
			Name:      StreamJobsPending,
			Subjects:  []string{SubjectJobsPending},
			Retention: jetstream.WorkQueuePolicy,
			MaxAge:    14 * 24 * time.Hour,
			Storage:   jetstream.FileStorage,
		},
		{
			Name:      StreamJobStatus,
			Subjects:  []string{SubjectJobStatus},
			Retention: jetstream.LimitsPolicy,
			MaxAge:    30 * 24 * time.Hour,
			Storage:   jetstream.FileStorage,
		},
		{
			Name:      StreamPublish,
			Subjects:  []string{SubjectPublishPattern},
			Retention: jetstream.WorkQueuePolicy,
			MaxAge:    3 * 24 * time.Hour,
			Storage:   jetstream.FileStorage,
		},
	}

	for _, cfg := range configs {
		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("failed to create/update stream %s: %w", cfg.Name, err)
		}
	}

	return nil
}

func ConnectJetStream(url string) (*nats.Conn, jetstream.JetStream, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, nil, fmt.Errorf("nats connection failed: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("failed to initialize jetstream: %w", err)
	}

	return nc, js, nil
}
