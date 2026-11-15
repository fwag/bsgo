package dispatcher

import (
	"testing"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestEnqueueJobIdempotentPerJobID(t *testing.T) {
	tests := []struct {
		name      string
		dispatch  bool
		wantQueue int
	}{
		{"queued job is not duplicated", false, 1},
		{"dispatched job is not queued again", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDispatcher(NewFuncPublisher(func(string, []byte) {}), time.Minute)
			job := &v1.BuildJob{JobID: "j1", Project: "p", Package: "a", Target: "t", Architecture: "x86_64"}
			d.EnqueueJob(job)
			if tt.dispatch {
				d.mu.Lock()
				d.queue.RemoveByBuildKey("p", "a", "t", "x86_64")
				d.dispatchedJobs["j1"] = job
				d.mu.Unlock()
			}
			d.EnqueueJob(job)
			if got := len(d.queue.Snapshot()); got != tt.wantQueue {
				t.Errorf("queue length %d, want %d", got, tt.wantQueue)
			}
		})
	}
}
