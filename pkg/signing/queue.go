package signing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
)

// Queue connects the signing stage to its two JetStream work queues. Requests
// (job-finished) and results (job-signed) are stored until a consumer
// acknowledges them, so a restart, a crashed signer or a lost connection never
// leaves a unit in state signing: an unacknowledged message is redelivered.
type Queue struct {
	JS jetstream.JetStream
	// AckWait is how long a request may stay unacknowledged without a
	// heartbeat before another replica gets it; the signer sends InProgress
	// at a third of it while a job runs. Default 30 minutes.
	AckWait time.Duration
	// MaxDeliver is the number of deliveries of one request before the job
	// fails for good; default 5.
	MaxDeliver int
	// Workers is the number of jobs a replica handles concurrently.
	Workers int
	// RetryDelay is how long a failed message waits before redelivery
	// (default 10s).
	RetryDelay time.Duration
	// Logf receives operational diagnostics; nil discards them.
	Logf func(format string, args ...any)
}

// Defaults of Queue.
const (
	DefaultAckWait    = 30 * time.Minute
	DefaultMaxDeliver = 5
	defaultRetryDelay = 10 * time.Second
)

func (q Queue) ackWait() time.Duration {
	if q.AckWait > 0 {
		return q.AckWait
	}
	return DefaultAckWait
}

func (q Queue) maxDeliver() int {
	if q.MaxDeliver > 0 {
		return q.MaxDeliver
	}
	return DefaultMaxDeliver
}

func (q Queue) retryDelay() time.Duration {
	if q.RetryDelay > 0 {
		return q.RetryDelay
	}
	return defaultRetryDelay
}

func (q Queue) logf(format string, args ...any) {
	if q.Logf != nil {
		q.Logf(format, args...)
	}
}

// Request stores a job-finished message. The job ID is the dedup ID, so a
// repeated request of the same job is stored once.
func (q Queue) Request(ctx context.Context, j Job) error {
	data, err := Encode(j)
	if err != nil {
		return err
	}
	_, err = q.JS.Publish(ctx, natsqueue.SubjectJobFinished, data, jetstream.WithMsgID(j.JobID+"/finished"))
	return err
}

// PublishAnswer stores a job-signed message.
func (q Queue) PublishAnswer(ctx context.Context, data []byte) error {
	j, err := Decode(data)
	if err != nil {
		return err
	}
	_, err = q.JS.Publish(ctx, natsqueue.SubjectJobSigned, data, jetstream.WithMsgID(j.JobID+"/signed"))
	return err
}

// StartSigner consumes job-finished messages with the shared durable consumer
// until ctx ends. A message is acknowledged after Stage.Handle returned, that
// is after the signed objects are uploaded and the answer is stored. A job
// that still fails on its last delivery gets a failure answer.
func (q Queue) StartSigner(ctx context.Context, stage *Stage) error {
	return q.start(ctx, natsqueue.StreamSignRequests, natsqueue.ConsumerSigner, natsqueue.SubjectJobFinished,
		q.maxDeliver(), func(ctx context.Context, m jetstream.Msg) { q.handleRequest(ctx, stage, m) })
}

// StartResults consumes job-signed messages with the shared durable consumer
// until ctx ends. A message is acknowledged after handle (which integrates the
// signed binaries) succeeded; failures are retried without limit because the
// handler is idempotent.
func (q Queue) StartResults(ctx context.Context, handle func(context.Context, []byte) error) error {
	return q.start(ctx, natsqueue.StreamSignResults, natsqueue.ConsumerSignResults, natsqueue.SubjectJobSigned,
		-1, func(ctx context.Context, m jetstream.Msg) { q.handleResult(ctx, handle, m) })
}

func (q Queue) start(ctx context.Context, stream, durable, subject string, maxDeliver int, h func(context.Context, jetstream.Msg)) error {
	cons, err := q.JS.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable: durable, FilterSubject: subject, AckPolicy: jetstream.AckExplicitPolicy,
		AckWait: q.ackWait(), MaxDeliver: maxDeliver,
	})
	if err != nil {
		return fmt.Errorf("creating consumer %s: %w", durable, err)
	}
	workers := max(q.Workers, 1)
	sem := make(chan struct{}, workers)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			h(ctx, m)
		}()
	}, jetstream.PullMaxMessages(workers))
	if err != nil {
		return fmt.Errorf("consuming %s: %w", durable, err)
	}
	go func() {
		<-ctx.Done()
		cc.Stop()
	}()
	return nil
}

// heartbeat tells the server every third of AckWait that the message is still
// being worked on; it returns the function that stops it.
func (q Queue) heartbeat(m jetstream.Msg) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(q.ackWait() / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := m.InProgress(); err != nil {
					q.logf("in-progress heartbeat failed: %v", err)
				}
			}
		}
	}()
	return func() { close(done) }
}

func (q Queue) handleRequest(ctx context.Context, stage *Stage, m jetstream.Msg) {
	stop := q.heartbeat(m)
	err := stage.Handle(ctx, m.Data())
	stop()
	if err == nil {
		q.settle(m.Ack(), "ack")
		return
	}
	if errors.Is(err, ErrMalformed) {
		q.logf("dropping malformed signing request: %v", err)
		q.settle(m.Term(), "term")
		return
	}
	if meta, merr := m.Metadata(); merr == nil && q.lastDelivery(meta.NumDelivered) {
		if ferr := stage.Fail(ctx, m.Data(), err); ferr == nil {
			q.settle(m.Ack(), "ack")
			return
		}
	}
	q.logf("signing request failed, will be redelivered: %v", err)
	q.settle(m.NakWithDelay(q.retryDelay()), "nak")
}

func (q Queue) handleResult(ctx context.Context, handle func(context.Context, []byte) error, m jetstream.Msg) {
	err := handle(ctx, m.Data())
	switch {
	case err == nil:
		q.settle(m.Ack(), "ack")
	case errors.Is(err, ErrMalformed):
		q.logf("dropping malformed signing answer: %v", err)
		q.settle(m.Term(), "term")
	default:
		q.logf("integrating signed job failed, will be redelivered: %v", err)
		q.settle(m.NakWithDelay(q.retryDelay()), "nak")
	}
}

// lastDelivery reports whether delivery number n is the last one allowed.
func (q Queue) lastDelivery(n uint64) bool {
	limit := q.maxDeliver()
	return limit > 0 && n >= uint64(limit)
}

func (q Queue) settle(err error, what string) {
	if err != nil {
		q.logf("%s of signing message failed: %v", what, err)
	}
}
