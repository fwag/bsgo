package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
)

// maxRemembered bounds the job IDs the sink counts publish attempts for.
const maxRemembered = 4096

// streamPublisher is the part of jetstream.JetStream the sink needs.
type streamPublisher interface {
	Publish(ctx context.Context, subject string, payload []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// jobSink is the checker's JobSink: it publishes jobs to the JOBS_PENDING
// stream and enqueues them in the local dispatcher.
type jobSink struct {
	js     streamPublisher
	nc     *nats.Conn
	disp   *dispatcher.Dispatcher
	bucket string

	mu       sync.Mutex
	attempts map[string]int // job ID -> times the job was emitted by this process
}

func newJobSink(js streamPublisher, nc *nats.Conn, disp *dispatcher.Dispatcher, bucket string) *jobSink {
	return &jobSink{js: js, nc: nc, disp: disp, bucket: bucket, attempts: map[string]int{}}
}

// Emit implements checker.JobSink. The dispatcher is the authority on whether
// a job is already in flight (queued, dispatched or reported running): such a
// job is only reprioritised. A job the dispatcher no longer knows is emitted
// again even if this process emitted its ID before, because an aborted job
// returns to scheduled with the same ID and must be dispatched again.
func (s *jobSink) Emit(ctx context.Context, spec *checker.JobSpec) error {
	if s.disp.Tracks(spec.JobID) {
		s.reprioritise(spec)
		return nil
	}
	job, err := jobFromSpec(spec, s.bucket, time.Now())
	if err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job %s: %w", job.JobID, err)
	}
	msgID := s.messageID(job.JobID)
	if _, err := s.js.Publish(ctx, natsqueue.SubjectJobsPending, data, jetstream.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("publish job %s: %w", job.JobID, err)
	}
	s.disp.EnqueueJob(job)
	logf("DISPATCHER", "enqueued job %s (%s/%s %s, priority %d, %s)",
		job.JobID, job.Project, job.Package, job.Target+"/"+job.Architecture, job.Priority, spec.Explain)
	return nil
}

// messageID returns the stream message ID of the next emission of a job. The
// first emission uses the job ID, so a restarted server does not publish
// duplicates; later ones carry the attempt number, so the stream's duplicate
// window never swallows the re-send of an aborted job.
func (s *jobSink) messageID(jobID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attempts) >= maxRemembered {
		clear(s.attempts)
	}
	n := s.attempts[jobID]
	s.attempts[jobID] = n + 1
	if n == 0 {
		return jobID
	}
	return fmt.Sprintf("%s#%d", jobID, n)
}

// reprioritise passes a re-emitted in-flight job to the dispatcher: a queued
// job takes a higher priority (a forced rebuild request with a higher tier),
// nothing is published again.
func (s *jobSink) reprioritise(spec *checker.JobSpec) {
	if s.disp.Queue().Raise(spec.JobID, spec.Priority) {
		logf("DISPATCHER", "raised job %s priority to %d", spec.JobID, spec.Priority)
	}
}

// Cancel implements checker.JobSink: the job leaves the dispatcher queue and a
// running build is told to abort.
func (s *jobSink) Cancel(_ context.Context, jobID, reason string) error {
	s.disp.Queue().RemoveJob(jobID)
	data, err := json.Marshal(v1.JobControl{JobID: jobID, Action: "ABORT", Reason: reason, TimestampUnix: time.Now().Unix()})
	if err != nil {
		return err
	}
	return s.nc.Publish(fmt.Sprintf("bsgo.jobs.%s.control", jobID), data)
}
