package dispatcher_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// The watchdog log line names the check that fired.
func TestReapReportsReason(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(disp *dispatcher.Dispatcher)
		silence time.Duration
		want    []string
	}{
		{
			name:    "silent worker that never reported the job",
			setup:   func(*dispatcher.Dispatcher) {},
			silence: 2 * reapTTL,
			want:    []string{"worker w1 silent for", "TTL 10s", "never reported running"},
		},
		{
			name:    "silent worker that had reported the job",
			setup:   func(d *dispatcher.Dispatcher) { d.ObserveActiveJobs("w1", []string{"j1"}) },
			silence: 3*reapTTL + time.Second,
			want:    []string{"worker w1 silent for", "last reported running"},
		},
		{
			name: "restarted worker that lost the job",
			setup: func(d *dispatcher.Dispatcher) {
				d.Registry().UpdateHeartbeat(idleHeartbeat("w1"))
				d.ObserveActiveJobs("w1", nil)
				d.SetDispatchGrace(0)
			},
			silence: time.Second,
			want:    []string{"worker w1 restarted", "reports no active jobs"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			disp, _ := dispatchOne(t, "j1")
			tc.setup(disp)
			got := disp.ReapStaleJobs(time.Now().Add(tc.silence))
			if len(got) != 1 || got[0].WorkerID != "w1" {
				t.Fatalf("reaped %+v, want j1 from w1", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got[0].Reason, w) {
					t.Errorf("reason %q lacks %q", got[0].Reason, w)
				}
			}
		})
	}
}

// Sequence of the 2026-11-20 incident on the dispatcher side: the worker is
// aborted, the job comes back with the same ID and must be dispatched again.
func TestAbortedJobIsDispatchedAgainWithTheSameID(t *testing.T) {
	disp, _ := dispatchOne(t, "j1")
	disp.RecordJobResult("j1", "w1", "p", 137, time.Second) // worker reports ABORTED
	if disp.Tracks("j1") {
		t.Fatal("dispatcher still tracks the aborted job")
	}
	disp.EnqueueJob(&v1.BuildJob{
		JobID: "j1", Project: "p", Package: "j1", Target: "std", Architecture: "x86_64",
		Resources: v1.ResourceRequirements{MemoryMB: 2048, VCPUs: 2},
	})
	disp.Registry().UpdateHeartbeat(idleHeartbeat("w1"))
	w, j, err := disp.DispatchNext(context.Background())
	if err != nil || j == nil || j.JobID != "j1" || w != "w1" {
		t.Fatalf("redispatch: %s %v %v", w, j, err)
	}
}
