package main

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
)

// recordingStream counts the jobs published to the stream.
type recordingStream struct {
	published int
}

func (r *recordingStream) Publish(context.Context, string, []byte, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	r.published++
	return &jetstream.PubAck{}, nil
}

func testSpec(id string, prio int32) *checker.JobSpec {
	return &checker.JobSpec{JobID: id, Project: "p", Repo: "std", Arch: "x86_64", Package: "a", Priority: prio}
}

func newTestSink(t *testing.T) (*jobSink, *dispatcher.Dispatcher, *recordingStream) {
	t.Helper()
	disp := dispatcher.NewDispatcher(dispatcher.NewFuncPublisher(func(string, []byte) {}), time.Minute)
	stream := &recordingStream{}
	return newJobSink(stream, nil, disp, "bucket"), disp, stream
}

func queuedPriority(disp *dispatcher.Dispatcher, id string) (int32, bool) {
	for _, j := range disp.Queue().Snapshot() {
		if j.JobID == id {
			return j.Priority, true
		}
	}
	return 0, false
}

func TestJobSinkEmitDedupeAgainstDispatcher(t *testing.T) {
	tests := []struct {
		name       string
		steps      func(t *testing.T, s *jobSink, disp *dispatcher.Dispatcher)
		wantQueued bool
		wantPrio   int32
		wantMsgs   int
	}{
		{
			name: "queued job is not enqueued twice",
			steps: func(t *testing.T, s *jobSink, _ *dispatcher.Dispatcher) {
				emit(t, s, testSpec("j1", 50))
				emit(t, s, testSpec("j1", 50))
			},
			wantQueued: true, wantPrio: 50, wantMsgs: 1,
		},
		{
			name: "queued job takes a higher priority",
			steps: func(t *testing.T, s *jobSink, _ *dispatcher.Dispatcher) {
				emit(t, s, testSpec("j1", 50))
				emit(t, s, testSpec("j1", 60))
			},
			wantQueued: true, wantPrio: 60, wantMsgs: 1,
		},
		{
			name: "aborted job that left the queue is dispatched again",
			steps: func(t *testing.T, s *jobSink, disp *dispatcher.Dispatcher) {
				emit(t, s, testSpec("j1", 50))
				disp.Queue().RemoveJob("j1") // dispatched, then aborted and finished
				emit(t, s, testSpec("j1", 50))
			},
			wantQueued: true, wantPrio: 50, wantMsgs: 2,
		},
		{
			name: "job reported running by a worker is left alone",
			steps: func(t *testing.T, s *jobSink, disp *dispatcher.Dispatcher) {
				disp.ObserveActiveJobs("w1", []string{"j1"})
				emit(t, s, testSpec("j1", 50))
			},
			wantQueued: false, wantMsgs: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, disp, stream := newTestSink(t)
			tt.steps(t, s, disp)
			prio, queued := queuedPriority(disp, "j1")
			if queued != tt.wantQueued || (queued && prio != tt.wantPrio) {
				t.Errorf("queued=%v priority=%d, want queued=%v priority=%d", queued, prio, tt.wantQueued, tt.wantPrio)
			}
			if stream.published != tt.wantMsgs {
				t.Errorf("published %d messages, want %d", stream.published, tt.wantMsgs)
			}
		})
	}
}

func TestJobSinkMessageIDs(t *testing.T) {
	s, _, _ := newTestSink(t)
	first, second, other := s.messageID("j1"), s.messageID("j1"), s.messageID("j2")
	if first != "j1" || other != "j2" {
		t.Errorf("first emissions use the job ID, got %q and %q", first, other)
	}
	if second == first {
		t.Errorf("a re-send after an abort must not reuse the message ID %q", first)
	}
}

func emit(t *testing.T, s *jobSink, spec *checker.JobSpec) {
	t.Helper()
	if err := s.Emit(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
}
