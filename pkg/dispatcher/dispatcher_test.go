package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestWorkerHeartbeatAndMatching(t *testing.T) {
	reg := dispatcher.NewWorkerRegistry(30 * time.Second)

	// Register a small worker (4GB RAM)
	reg.UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:     "worker-small-01",
		Architecture: "x86_64",
		Status:       "IDLE",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      2,
			TotalMemoryMB:   4096,
			AvailableDiskGB: 20,
			SandboxType:     "kvm",
		},
		CachedRPMHashes: []string{"hash-glibc", "hash-bash"},
		TimestampUnix:   time.Now().Unix(),
	})

	// Register a large worker (32GB RAM)
	reg.UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:     "worker-large-01",
		Architecture: "x86_64",
		Status:       "IDLE",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      16,
			TotalMemoryMB:   32768,
			AvailableDiskGB: 100,
			SandboxType:     "kvm",
		},
		CachedRPMHashes: []string{"hash-glibc", "hash-gcc", "hash-llvm"},
		TimestampUnix:   time.Now().Unix(),
	})

	// 1. Job requiring 16GB RAM -> should only match worker-large-01
	jobHeavy := &v1.BuildJob{
		JobID:        "job-heavy-01",
		Architecture: "x86_64",
		Resources: v1.ResourceRequirements{
			MemoryMB:    16384,
			VCPUs:       8,
			DiskGB:      50,
			SandboxType: "kvm",
		},
	}

	worker, score := reg.FindBestIdleWorker(jobHeavy)
	if worker == nil {
		t.Fatalf("expected to find a matching worker for heavy job")
	}
	if worker.WorkerID != "worker-large-01" {
		t.Errorf("expected worker-large-01, got %s", worker.WorkerID)
	}

	// 2. Job requiring 2GB RAM with warm-cache preference
	jobSmall := &v1.BuildJob{
		JobID:        "job-small-01",
		Architecture: "x86_64",
		Resources: v1.ResourceRequirements{
			MemoryMB:    2048,
			VCPUs:       2,
			DiskGB:      10,
			SandboxType: "kvm",
		},
		ChrootRPMs: []v1.ChrootRPM{
			{Filename: "gcc.rpm", SHA256: "hash-gcc"},
			{Filename: "llvm.rpm", SHA256: "hash-llvm"},
		},
	}

	worker2, score2 := reg.FindBestIdleWorker(jobSmall)
	if worker2 == nil {
		t.Fatalf("expected to find worker for small job")
	}
	// worker-large-01 has both hashes (2/2 = 1.0), worker-small has 0/2 = 0.0
	if worker2.WorkerID != "worker-large-01" {
		t.Errorf("expected worker-large-01 due to cache affinity, got %s", worker2.WorkerID)
	}
	if score2 != 1.0 {
		t.Errorf("expected perfect cache score 1.0, got %f", score2)
	}
	_ = score
}

func TestTwoInterdependentProjectsPoC(t *testing.T) {
	ctx := context.Background()

	// Mock publisher to record dispatches and aborts
	dispatchedJobs := make(map[string]*v1.BuildJob)
	pub := dispatcher.NewMockPublisher(func(subj string, data []byte) {
		// Callback for published events
	})

	disp := dispatcher.NewDispatcher(pub, 30*time.Second)

	// Register 1 idle worker
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:     "worker-x86-01",
		Architecture: "x86_64",
		Status:       "IDLE",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      8,
			TotalMemoryMB:   16384,
			AvailableDiskGB: 50,
			SandboxType:     "kvm",
		},
		CachedRPMHashes: []string{"hash-glibc"},
		TimestampUnix:   time.Now().Unix(),
	})

	// Project A (base:core): libgreeting (Priority: 100 - Blocker)
	jobLibGreeting := &v1.BuildJob{
		JobID:        "job-base-libgreeting",
		Project:      "base:core",
		Package:      "libgreeting",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     100, // Critical-path blocker!
		ChrootRPMs: []v1.ChrootRPM{
			{Filename: "glibc.rpm", SHA256: "hash-glibc"},
		},
		Resources: v1.ResourceRequirements{
			MemoryMB:    4096,
			VCPUs:       4,
			DiskGB:      20,
			SandboxType: "kvm",
		},
	}

	// Project B (apps:tools): hello-world (Priority: 20 - Consumer depending on libgreeting)
	jobHelloWorld := &v1.BuildJob{
		JobID:        "job-apps-hello-world",
		Project:      "apps:tools",
		Package:      "hello-world",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     20, // Lower priority, waiting on base:core
		ChrootRPMs: []v1.ChrootRPM{
			{Filename: "glibc.rpm", SHA256: "hash-glibc"},
			{Filename: "libgreeting.rpm", SHA256: "hash-libgreeting"},
		},
		Resources: v1.ResourceRequirements{
			MemoryMB:    2048,
			VCPUs:       2,
			DiskGB:      10,
			SandboxType: "kvm",
		},
	}

	// Enqueue hello-world first, then libgreeting second
	disp.EnqueueJob(jobHelloWorld)
	disp.EnqueueJob(jobLibGreeting)

	// 1. Dispatch round: Since only 1 worker is available, libgreeting MUST be dispatched first
	// because it has priority 100 vs 20!
	assignedWorker, dispatchedJob, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}
	if dispatchedJob.JobID != "job-base-libgreeting" {
		t.Fatalf("critical-path violation: expected job-base-libgreeting first, got %s", dispatchedJob.JobID)
	}
	if assignedWorker != "worker-x86-01" {
		t.Errorf("expected assigned to worker-x86-01, got %s", assignedWorker)
	}

	// 2. While worker is busy, dispatch next should return nil (no idle workers)
	busyWorker, busyJob, _ := disp.DispatchNext(ctx)
	if busyJob != nil {
		t.Fatalf("expected no job dispatched while worker is busy, got: %v", busyJob.JobID)
	}
	_ = busyWorker

	// 3. Worker finishes libgreeting and reports heartbeat with new cached hash (hash-libgreeting)!
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:        "worker-x86-01",
		Architecture:    "x86_64",
		Status:          "IDLE", // Worker is idle again
		CachedRPMHashes: []string{"hash-glibc", "hash-libgreeting"},
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      8,
			TotalMemoryMB:   16384,
			AvailableDiskGB: 50,
			SandboxType:     "kvm",
		},
		TimestampUnix: time.Now().Unix(),
	})

	// 4. Now hello-world is dispatched to the same worker with 100% warm-cache affinity!
	w2, j2, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("failed to dispatch hello-world: %v", err)
	}
	if j2.JobID != "job-apps-hello-world" {
		t.Fatalf("expected job-apps-hello-world, got %s", j2.JobID)
	}
	if w2 != "worker-x86-01" {
		t.Errorf("expected worker-x86-01, got %s", w2)
	}

	dispatchedJobs[dispatchedJob.JobID] = dispatchedJob
	dispatchedJobs[j2.JobID] = j2
}

func TestSupersededCommitCancellation(t *testing.T) {
	ctx := context.Background()

	var abortedJob string
	pub := dispatcher.NewMockPublisher(func(subj string, data []byte) {
		// Check for control abort
	})

	disp := dispatcher.NewDispatcher(pub, 30*time.Second)
	disp.OnAbort(func(jobID, reason string) {
		abortedJob = jobID
	})

	// Register idle worker
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:     "worker-01",
		Architecture: "x86_64",
		Status:       "IDLE",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:    4,
			TotalMemoryMB: 8192,
			SandboxType:   "kvm",
		},
		TimestampUnix: time.Now().Unix(),
	})

	// Commit 1 arrives
	jobV1 := &v1.BuildJob{
		JobID:        "job-v1",
		Project:      "openSUSE:Factory",
		Package:      "ripgrep",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     50,
	}
	disp.EnqueueJob(jobV1)
	_, _, _ = disp.DispatchNext(ctx)

	// Worker is building v1. Now commit 2 arrives for the exact same package/target/arch!
	jobV2 := &v1.BuildJob{
		JobID:        "job-v2",
		Project:      "openSUSE:Factory",
		Package:      "ripgrep",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     50,
	}
	disp.EnqueueJob(jobV2)

	// Assert that job-v1 was immediately signaled to abort!
	if abortedJob != "job-v1" {
		t.Errorf("expected job-v1 to be aborted, got: %s", abortedJob)
	}
}

func TestDispatcherMultiJobCapacityBinPacking(t *testing.T) {
	ctx := context.Background()
	mockPub := dispatcher.NewMockPublisher(nil)
	disp := dispatcher.NewDispatcher(mockPub, 30*time.Second)

	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:     "worker-multi-01",
		Architecture: "x86_64",
		Status:       "IDLE",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:        8,
			TotalMemoryMB:     16384,
			AvailableVCPUs:    8,
			AvailableMemoryMB: 16384,
			AvailableDiskGB:   100,
			SandboxType:       "kvm",
		},
		TimestampUnix: time.Now().Unix(),
	})

	job1 := &v1.BuildJob{
		JobID:        "job-1",
		Project:      "prjA",
		Package:      "pkg1",
		Architecture: "x86_64",
		Priority:     10,
		Resources:    v1.ResourceRequirements{MemoryMB: 4096, VCPUs: 2, DiskGB: 10, SandboxType: "kvm"},
	}
	job2 := &v1.BuildJob{
		JobID:        "job-2",
		Project:      "prjA",
		Package:      "pkg2",
		Architecture: "x86_64",
		Priority:     10,
		Resources:    v1.ResourceRequirements{MemoryMB: 4096, VCPUs: 2, DiskGB: 10, SandboxType: "kvm"},
	}
	job3 := &v1.BuildJob{
		JobID:        "job-3",
		Project:      "prjA",
		Package:      "pkg3",
		Architecture: "x86_64",
		Priority:     10,
		Resources:    v1.ResourceRequirements{MemoryMB: 8192, VCPUs: 4, DiskGB: 10, SandboxType: "kvm"},
	}
	job4 := &v1.BuildJob{
		JobID:        "job-4",
		Project:      "prjA",
		Package:      "pkg4",
		Architecture: "x86_64",
		Priority:     10,
		Resources:    v1.ResourceRequirements{MemoryMB: 4096, VCPUs: 2, DiskGB: 10, SandboxType: "kvm"},
	}

	disp.EnqueueJob(job1)
	disp.EnqueueJob(job2)
	disp.EnqueueJob(job3)
	disp.EnqueueJob(job4)

	// 1. Dispatch job 1 (4GB, 2 vCPUs) -> fits
	w1, j1, err := disp.DispatchNext(ctx)
	if err != nil || j1 == nil || w1 != "worker-multi-01" {
		t.Fatalf("failed dispatching job 1: w=%s, j=%v, err=%v", w1, j1, err)
	}

	// 2. Dispatch job 2 (4GB, 2 vCPUs) -> fits concurrently on same worker!
	w2, j2, err := disp.DispatchNext(ctx)
	if err != nil || j2 == nil || w2 != "worker-multi-01" {
		t.Fatalf("failed dispatching job 2 concurrently: w=%s, j=%v, err=%v", w2, j2, err)
	}

	// 3. Dispatch job 3 (8GB, 4 vCPUs) -> fits remaining capacity (8GB, 4 vCPUs)!
	w3, j3, err := disp.DispatchNext(ctx)
	if err != nil || j3 == nil || w3 != "worker-multi-01" {
		t.Fatalf("failed dispatching job 3: w=%s, j=%v, err=%v", w3, j3, err)
	}

	// 4. Dispatch job 4 (4GB, 2 vCPUs) -> capacity fully saturated (0GB, 0 vCPUs left), must NOT dispatch
	w4, j4, _ := disp.DispatchNext(ctx)
	if j4 != nil {
		t.Fatalf("job 4 should NOT be dispatched while worker capacity is exhausted, got: %s on %s", j4.JobID, w4)
	}

	// 5. Complete job 1 -> releases 4GB and 2 vCPUs
	disp.RecordJobResult("job-1", "worker-multi-01", "prjA", 0, 5*time.Second)

	// 6. Now job 4 fits into the released capacity!
	w4, j4, err = disp.DispatchNext(ctx)
	if err != nil || j4 == nil || w4 != "worker-multi-01" || j4.JobID != "job-4" {
		t.Fatalf("job 4 should be dispatched after job 1 releases capacity: w=%s, j=%v, err=%v", w4, j4, err)
	}
}
