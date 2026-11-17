package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

const reapTTL = 10 * time.Second

func idleHeartbeat(id string) v1.WorkerHeartbeat {
	return v1.WorkerHeartbeat{
		WorkerID: id, Architecture: "x86_64", Status: "IDLE",
		Hardware: v1.WorkerHardware{TotalVCPUs: 8, AvailableVCPUs: 8, TotalMemoryMB: 16384, AvailableMemoryMB: 16384},
	}
}

// dispatchOne registers a worker and dispatches one job to it.
func dispatchOne(t *testing.T, jobID string) (*dispatcher.Dispatcher, *[]string) {
	t.Helper()
	var aborts []string
	pub := dispatcher.NewMockPublisher(func(subj string, _ []byte) {
		aborts = append(aborts, subj)
	})
	disp := dispatcher.NewDispatcher(pub, reapTTL)
	disp.Registry().UpdateHeartbeat(idleHeartbeat("w1"))
	disp.EnqueueJob(&v1.BuildJob{
		JobID: jobID, Project: "p", Package: jobID, Target: "std", Architecture: "x86_64",
		Resources: v1.ResourceRequirements{MemoryMB: 2048, VCPUs: 2},
	})
	if w, j, err := disp.DispatchNext(context.Background()); err != nil || j == nil || w != "w1" {
		t.Fatalf("dispatch: %s %v %v", w, j, err)
	}
	return disp, &aborts
}

// A heartbeat that left the worker before the job message arrived reports no
// active job. Under load that is not a restart.
func TestReapIgnoresHeartbeatThatPredatesTheJob(t *testing.T) {
	disp, _ := dispatchOne(t, "j1")
	disp.Registry().UpdateHeartbeat(idleHeartbeat("w1"))
	disp.ObserveActiveJobs("w1", nil)

	if got := disp.ReapStaleJobs(time.Now().Add(time.Second)); len(got) != 0 {
		t.Fatalf("reaped a job dispatched a second ago: %v", got)
	}
	if disp.Queue().Len() != 0 {
		t.Fatal("job went back into the queue")
	}
}

func TestReapKeepsJobsTheWorkerReportsRunning(t *testing.T) {
	tests := []struct {
		name      string
		silence   time.Duration // how long the worker is silent
		wantReaps int
	}{
		{"worker slow to heartbeat under load", 2 * reapTTL, 0},
		{"worker silent for three TTLs is dead", 3*reapTTL + time.Second, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			disp, aborts := dispatchOne(t, "j1")
			disp.Registry().UpdateHeartbeat(idleHeartbeat("w1"))
			disp.ObserveActiveJobs("w1", []string{"j1"})

			got := disp.ReapStaleJobs(time.Now().Add(tc.silence))
			if len(got) != tc.wantReaps {
				t.Fatalf("reaped %d jobs, want %d", len(got), tc.wantReaps)
			}
			if tc.wantReaps == 0 && len(*aborts) != 1 { // only the dispatch itself
				t.Errorf("abort sent for a running job: %v", *aborts)
			}
		})
	}
}

// The same job must not be sent again while its worker still runs it.
func TestDispatchSkipsJobsRunningOnAWorker(t *testing.T) {
	disp, _ := dispatchOne(t, "j1")
	disp.ObserveActiveJobs("w1", []string{"j1"})
	disp.Queue().Push(&v1.BuildJob{
		JobID: "j1", Project: "p", Package: "j1", Target: "std", Architecture: "x86_64",
		Resources: v1.ResourceRequirements{MemoryMB: 1024, VCPUs: 1},
	})

	w, j, err := disp.DispatchNext(context.Background())
	if err != nil || j != nil {
		t.Fatalf("dispatched %v to %s again: %v", j, w, err)
	}
	if disp.Queue().Len() != 0 {
		t.Fatal("running job stayed queued")
	}
}
