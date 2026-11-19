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

// maxRemembered bounds the job IDs the sink remembers for deduplication.
const maxRemembered = 4096

// jobSink is the checker's JobSink: it publishes jobs to the JOBS_PENDING
// stream (deduplicated by job ID) and enqueues them in the local dispatcher.
type jobSink struct {
	js     jetstream.JetStream
	nc     *nats.Conn
	disp   *dispatcher.Dispatcher
	bucket string

	mu   sync.Mutex
	seen map[string]int32 // emitted job ID -> highest priority emitted
}

func newJobSink(js jetstream.JetStream, nc *nats.Conn, disp *dispatcher.Dispatcher, bucket string) *jobSink {
	return &jobSink{js: js, nc: nc, disp: disp, bucket: bucket, seen: map[string]int32{}}
}

// Emit implements checker.JobSink. A job ID that was already emitted by this
// process is ignored; across restarts the stream deduplicates by message ID.
func (s *jobSink) Emit(ctx context.Context, spec *checker.JobSpec) error {
	s.mu.Lock()
	prio, dup := s.seen[spec.JobID]
	if dup && spec.Priority > prio {
		s.seen[spec.JobID] = spec.Priority
	}
	s.mu.Unlock()
	if dup {
		return s.reprioritise(spec, prio)
	}
	job, err := jobFromSpec(spec, s.bucket, time.Now())
	if err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job %s: %w", job.JobID, err)
	}
	if _, err := s.js.Publish(ctx, natsqueue.SubjectJobsPending, data, jetstream.WithMsgID(job.JobID)); err != nil {
		return fmt.Errorf("publish job %s: %w", job.JobID, err)
	}
	s.disp.EnqueueJob(job)
	s.mu.Lock()
	if len(s.seen) >= maxRemembered {
		clear(s.seen)
	}
	s.seen[spec.JobID] = spec.Priority
	s.mu.Unlock()
	logf("DISPATCHER", "enqueued job %s (%s/%s %s, priority %d, %s)",
		job.JobID, job.Project, job.Package, job.Target+"/"+job.Architecture, job.Priority, spec.Explain)
	return nil
}

// reprioritise passes a re-emitted job to the dispatcher when its priority
// rose (a forced rebuild request with a higher tier): the queued job takes the
// priority, nothing is published again.
func (s *jobSink) reprioritise(spec *checker.JobSpec, was int32) error {
	if spec.Priority <= was {
		return nil
	}
	job, err := jobFromSpec(spec, s.bucket, time.Now())
	if err != nil {
		return err
	}
	s.disp.EnqueueJob(job)
	logf("DISPATCHER", "raised job %s priority %d -> %d", job.JobID, was, job.Priority)
	return nil
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
