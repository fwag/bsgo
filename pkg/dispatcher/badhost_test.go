package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestFailureClassification(t *testing.T) {
	tests := []struct {
		exitCode int
		isHost   bool
	}{
		{0, false},   // Success
		{1, false},   // Package recipe failure (syntax/compiler error)
		{137, false}, // Cancelled/aborted
		{2, true},    // Preinstall / missing repo failure
		{3, true},    // Chroot setup / disk full failure
		{4, true},    // OOM failure
		{9, true},    // Micro-VM hypervisor crash
	}

	for _, tt := range tests {
		got := dispatcher.IsHostInfrastructureFailure(tt.exitCode)
		if got != tt.isHost {
			t.Errorf("IsHostInfrastructureFailure(%d) = %v, want %v", tt.exitCode, got, tt.isHost)
		}
	}
}

func TestBadHostTrackingAndRetryFiltering(t *testing.T) {
	ctx := context.Background()
	disp := dispatcher.NewDispatcher(dispatcher.NewMockPublisher(nil), 30*time.Second)

	// Register two idle workers: worker-1 and worker-2
	now := time.Now()
	for _, id := range []string{"worker-1", "worker-2"} {
		disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
			WorkerID:     id,
			Architecture: "x86_64",
			Status:       "IDLE",
			Hardware: v1.WorkerHardware{
				TotalVCPUs:    4,
				TotalMemoryMB: 8192,
			},
			TimestampUnix: now.Unix(),
		})
	}

	job := &v1.BuildJob{
		JobID:        "job-flaky",
		Project:      "openSUSE:Factory",
		Package:      "kernel",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     100,
	}

	// First dispatch: finds an idle worker (e.g. worker-1)
	disp.EnqueueJob(job)
	w1, _, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}

	// Worker 1 encounters an infrastructure error (exit code 9: hypervisor crash)
	disp.RecordJobResult("job-flaky", w1, "openSUSE:Factory", 9, 5*time.Second)

	// Set worker 1 back to IDLE
	disp.Registry().SetWorkerStatus(w1, "IDLE", "")

	// Re-enqueue the exact same job for retry
	disp.EnqueueJob(job)

	// Second dispatch: worker 1 is a badhost for job-flaky! Dispatcher MUST assign to worker-2!
	w2, _, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("second dispatch failed: %v", err)
	}

	if w2 == w1 {
		t.Fatalf("expected job to bypass badhost %s and dispatch to alternative worker, but got %s", w1, w2)
	}
}

func TestWorkerQuarantineAndRecovery(t *testing.T) {
	// Quarantine duration: 100ms, maxConsecutiveErrors: 2
	reg := dispatcher.NewWorkerRegistryWithConfig(30*time.Second, 100*time.Millisecond, 2)

	now := time.Now()
	reg.UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:      "worker-faulty",
		Architecture:  "x86_64",
		Status:        "IDLE",
		TimestampUnix: now.Unix(),
	})

	jobA := &v1.BuildJob{JobID: "job-a", Architecture: "x86_64"}
	jobB := &v1.BuildJob{JobID: "job-b", Architecture: "x86_64"}

	// First infrastructure error: count becomes 1 (not yet quarantined)
	reg.RecordJobFailure(jobA.JobID, "worker-faulty", 3, now)
	w, _ := reg.FindBestIdleWorker(jobB)
	if w == nil {
		t.Fatalf("worker should still be available after 1 error")
	}

	// Second infrastructure error: threshold reached! Status becomes QUARANTINE
	reg.RecordJobFailure(jobB.JobID, "worker-faulty", 9, now)
	w, _ = reg.FindBestIdleWorker(jobB)
	if w != nil {
		t.Fatalf("worker should be in QUARANTINE and unavailable for dispatch")
	}

	// Wait for quarantine duration (100ms) to expire
	time.Sleep(120 * time.Millisecond)

	// Worker should automatically recover and be available again!
	jobC := &v1.BuildJob{JobID: "job-c", Architecture: "x86_64"}
	w, _ = reg.FindBestIdleWorker(jobC)
	if w == nil {
		t.Fatalf("worker should have recovered from quarantine after timeout")
	}
	if w.WorkerID != "worker-faulty" {
		t.Errorf("expected worker-faulty, got %s", w.WorkerID)
	}
}

func TestJobHostExhaustion(t *testing.T) {
	reg := dispatcher.NewWorkerRegistry(30 * time.Second)

	now := time.Now()
	// Fail job on 3 distinct workers
	reg.RecordJobFailure("job-impossible", "worker-1", 3, now)
	reg.RecordJobFailure("job-impossible", "worker-2", 9, now)
	if reg.IsJobHostExhausted("job-impossible", 3) {
		t.Errorf("should not be exhausted after 2 workers")
	}

	reg.RecordJobFailure("job-impossible", "worker-3", 2, now)
	if !reg.IsJobHostExhausted("job-impossible", 3) {
		t.Errorf("job should be host-exhausted after 3 distinct worker failures")
	}
}
