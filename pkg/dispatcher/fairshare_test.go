package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestFairShareExponentialDecayLoad(t *testing.T) {
	// Half-life: 10 seconds -> after 10s load should be 50%
	fsm := dispatcher.NewFairShareManager(10*time.Second, 0.05, 100.0, 0.01)

	t0 := time.Now()
	// Record 100 seconds of CPU build time at t0
	fsm.RecordBuildTime("project-a", 100*time.Second, t0)

	initialLoad := fsm.GetProjectLoad("project-a", t0)
	if initialLoad != 100.0 {
		t.Fatalf("expected initial load 100.0, got %f", initialLoad)
	}

	// At t0 + 10s, decayed load should be approximately 50.0
	t1 := t0.Add(10 * time.Second)
	decayedLoad := fsm.GetProjectLoad("project-a", t1)
	if decayedLoad < 49.0 || decayedLoad > 51.0 {
		t.Errorf("expected decayed load around 50.0, got %f", decayedLoad)
	}

	// At t0 + 20s, decayed load should be approximately 25.0
	t2 := t0.Add(20 * time.Second)
	decayedLoad2 := fsm.GetProjectLoad("project-a", t2)
	if decayedLoad2 < 24.0 || decayedLoad2 > 26.0 {
		t.Errorf("expected decayed load around 25.0, got %f", decayedLoad2)
	}
}

func TestFairShareStalenessAging(t *testing.T) {
	// Aging rate: 1.0 points per second
	fsm := dispatcher.NewFairShareManager(30*time.Minute, 1.0, 100.0, 0.01)

	now := time.Now()

	// Job A: created 100 seconds ago, base priority 50 -> effective: 50 + 100 = 150
	jobA := &v1.BuildJob{
		JobID:         "job-old",
		Project:       "prj",
		Priority:      50,
		CreatedAtUnix: now.Add(-100 * time.Second).Unix(),
	}

	// Job B: created 0 seconds ago, higher base priority 100 -> effective: 100 + 0 = 100
	jobB := &v1.BuildJob{
		JobID:         "job-new",
		Project:       "prj",
		Priority:      100,
		CreatedAtUnix: now.Unix(),
	}

	jobs := []*v1.BuildJob{jobB, jobA}
	fsm.SortJobsFairShare(jobs, now)

	// Due to staleness aging, jobA (150) MUST be sorted ahead of jobB (100)
	if jobs[0].JobID != "job-old" {
		t.Errorf("expected job-old to be prioritized due to staleness aging, got %s", jobs[0].JobID)
	}
}

func TestFairShareSourceChangeBias(t *testing.T) {
	// Source bias: +100.0
	fsm := dispatcher.NewFairShareManager(30*time.Minute, 0.0, 100.0, 0.01)
	now := time.Now()

	// Cascade rebuild job with higher base priority 120 -> effective: 120
	jobCascade := &v1.BuildJob{
		JobID:         "job-cascade",
		Project:       "prj",
		Priority:      120,
		Reason:        "CASCADE",
		CreatedAtUnix: now.Unix(),
	}

	// Direct user commit with base priority 50 -> effective: 50 + 100 (bias) = 150
	jobHuman := &v1.BuildJob{
		JobID:         "job-human",
		Project:       "prj",
		Priority:      50,
		Reason:        "SOURCE_CHANGE",
		CreatedAtUnix: now.Unix(),
	}

	jobs := []*v1.BuildJob{jobCascade, jobHuman}
	fsm.SortJobsFairShare(jobs, now)

	if jobs[0].JobID != "job-human" {
		t.Errorf("expected job-human to be prioritized due to source change bias, got %s", jobs[0].JobID)
	}
}

func TestDispatcherFairShareInterleaving(t *testing.T) {
	ctx := context.Background()
	disp := dispatcher.NewDispatcher(dispatcher.NewMockPublisher(nil), 30*time.Second)

	// Register 1 idle worker
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:     "worker-1",
		Architecture: "x86_64",
		Status:       "IDLE",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:    4,
			TotalMemoryMB: 8192,
		},
		TimestampUnix: time.Now().Unix(),
	})

	// Heavy project has accumulated 10,000s of build time (load penalty = 100)
	disp.FairShare().RecordBuildTime("heavy-prj", 10000*time.Second, time.Now())

	// Enqueue job from heavy-prj (Base priority: 100 -> effective: 100 - 100 = 0)
	disp.EnqueueJob(&v1.BuildJob{
		JobID:         "job-heavy",
		Project:       "heavy-prj",
		Package:       "pkg1",
		Target:        "standard",
		Architecture:  "x86_64",
		Priority:      100,
		CreatedAtUnix: time.Now().Unix(),
	})

	// Enqueue job from light-prj (Base priority: 50 -> effective: 50 - 0 = 50)
	disp.EnqueueJob(&v1.BuildJob{
		JobID:         "job-light",
		Project:       "light-prj",
		Package:       "pkg2",
		Target:        "standard",
		Architecture:  "x86_64",
		Priority:      50,
		CreatedAtUnix: time.Now().Unix(),
	})

	// DispatchNext should select job-light over job-heavy despite lower base priority!
	workerID, dispatchedJob, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("unexpected error during dispatch: %v", err)
	}
	if workerID != "worker-1" {
		t.Errorf("expected worker-1, got %s", workerID)
	}
	if dispatchedJob.JobID != "job-light" {
		t.Errorf("expected job-light to be dispatched due to fair-share penalty on heavy-prj, got %s", dispatchedJob.JobID)
	}
}
