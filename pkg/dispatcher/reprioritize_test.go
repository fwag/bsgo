package dispatcher_test

import (
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestReEnqueueRaisesPriorityWithoutDuplicate(t *testing.T) {
	tests := []struct {
		name     string
		resent   int32
		wantPrio int32
	}{
		{"higher priority is taken", 20050, 20050},
		{"same priority is kept", 60, 60},
		{"lower priority does not lower it", 55, 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disp := dispatcher.NewDispatcher(dispatcher.NewMockPublisher(nil), 30*time.Second)
			other := &v1.BuildJob{JobID: "other", Project: "p", Package: "other", Target: "std", Architecture: "x86_64", Priority: 100}
			job := &v1.BuildJob{JobID: "j1", Project: "p", Package: "a", Target: "std", Architecture: "x86_64", Priority: 60}
			disp.EnqueueJob(other)
			disp.EnqueueJob(job)
			resent := *job
			resent.Priority = tt.resent
			disp.EnqueueJob(&resent)

			q := disp.Queue()
			if q.Len() != 2 {
				t.Fatalf("queue has %d jobs, want 2", q.Len())
			}
			var got int32
			for _, j := range q.Snapshot() {
				if j.JobID == "j1" {
					got = j.Priority
				}
			}
			if got != tt.wantPrio {
				t.Errorf("priority %d, want %d", got, tt.wantPrio)
			}
			if first := q.Peek(); (tt.wantPrio > 100) != (first.JobID == "j1") {
				t.Errorf("queue head is %s", first.JobID)
			}
		})
	}
}
