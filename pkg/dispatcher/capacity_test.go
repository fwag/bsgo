package dispatcher

import (
	"context"
	"testing"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func capWorker(id string, freeVCPUs int32, freeMemMB int64, active ...string) v1.WorkerHeartbeat {
	return v1.WorkerHeartbeat{
		WorkerID: id, Status: "IDLE", Architecture: "x86_64", ActiveJobIDs: active,
		Hardware: v1.WorkerHardware{
			TotalVCPUs: 8, AvailableVCPUs: freeVCPUs, TotalMemoryMB: 30720, AvailableMemoryMB: freeMemMB,
			SandboxType: "nspawn",
		},
	}
}

func capJob(id string, vcpus int32, memMB int64) *v1.BuildJob {
	return &v1.BuildJob{
		JobID: id, Project: "p", Package: id, Target: "t", Architecture: "x86_64",
		Resources: v1.ResourceRequirements{VCPUs: vcpus, MemoryMB: memMB},
	}
}

func TestMatchesConstraintsFullWorkerHasNoRoom(t *testing.T) {
	tests := []struct {
		name string
		hw   v1.WorkerHardware
		req  v1.ResourceRequirements
		want bool
	}{
		{"fully allocated", v1.WorkerHardware{TotalVCPUs: 8, TotalMemoryMB: 16384}, v1.ResourceRequirements{VCPUs: 2, MemoryMB: 2048}, false},
		{"no vCPUs left", v1.WorkerHardware{TotalVCPUs: 8, TotalMemoryMB: 16384, AvailableMemoryMB: 8192}, v1.ResourceRequirements{VCPUs: 2, MemoryMB: 2048}, false},
		{"no memory left", v1.WorkerHardware{TotalVCPUs: 8, TotalMemoryMB: 16384, AvailableVCPUs: 4}, v1.ResourceRequirements{VCPUs: 2, MemoryMB: 2048}, false},
		{"fits", v1.WorkerHardware{TotalVCPUs: 8, TotalMemoryMB: 16384, AvailableVCPUs: 2, AvailableMemoryMB: 2048}, v1.ResourceRequirements{VCPUs: 2, MemoryMB: 2048}, true},
		{"no capacity stated", v1.WorkerHardware{}, v1.ResourceRequirements{VCPUs: 2, MemoryMB: 2048}, true},
	}
	for _, tt := range tests {
		if got := matchesConstraints(&tt.hw, &tt.req); got != tt.want {
			t.Errorf("%s: matches=%v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPendingAllocationsLimitDispatch(t *testing.T) {
	tests := []struct {
		name string
		// heartbeat after the dispatch of the first job; nil sends none
		heartbeat func() v1.WorkerHeartbeat
		advance   time.Duration
		wantNext  bool
	}{
		{"stale heartbeat does not free the capacity", func() v1.WorkerHeartbeat { return capWorker("w", 2, 10240) }, 0, false},
		{"heartbeat listing the job keeps the deduction", func() v1.WorkerHeartbeat { return capWorker("w", 0, 10240, "big") }, 0, false},
		{"acknowledged job: heartbeat availability is the truth", func() v1.WorkerHeartbeat { return capWorker("w", 2, 10240, "big") }, 0, true},
		{"unacknowledged job times out", func() v1.WorkerHeartbeat { return capWorker("w", 2, 10240) }, 3 * time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			disp := NewDispatcher(NewMockPublisher(nil), time.Hour)
			disp.registry.SetClock(clock.now)
			disp.registry.UpdateHeartbeat(capWorker("w", 8, 30720))
			disp.EnqueueJob(capJob("big", 6, 20480))
			if _, j, _ := disp.DispatchNext(context.Background()); j == nil {
				t.Fatal("big job not placed on the empty worker")
			}
			clock.advance(tt.advance)
			disp.registry.UpdateHeartbeat(tt.heartbeat())
			disp.EnqueueJob(capJob("small", 2, 4096))
			_, j, _ := disp.DispatchNext(context.Background())
			if (j != nil) != tt.wantNext {
				t.Fatalf("second job placed=%v, want %v", j != nil, tt.wantNext)
			}
		})
	}
}

func TestFullWorkerGetsNoMoreAfterRemainingRoomUsed(t *testing.T) {
	disp := NewDispatcher(NewMockPublisher(nil), time.Hour)
	disp.registry.UpdateHeartbeat(capWorker("w", 8, 30720))
	for _, j := range []*v1.BuildJob{capJob("big", 6, 20480), capJob("fits", 2, 8192), capJob("over", 1, 1024)} {
		disp.EnqueueJob(j)
	}
	var placed []string
	for i := 0; i < 3; i++ {
		if _, j, _ := disp.DispatchNext(context.Background()); j != nil {
			placed = append(placed, j.JobID)
		}
	}
	if len(placed) != 2 {
		t.Fatalf("placed %v, want the big job and the one filling the remaining 2 vCPUs", placed)
	}
}

func TestReleaseAndRefusalClearPending(t *testing.T) {
	tests := []struct {
		name string
		end  func(d *Dispatcher)
	}{
		{"job finished", func(d *Dispatcher) { d.RecordJobResult("big", "w", "p", 0, time.Second) }},
		{"job failed", func(d *Dispatcher) { d.RecordJobResult("big", "w", "p", 1, time.Second) }},
		{"worker refused it", func(d *Dispatcher) {
			if !d.RequeueRefused("big", "w") {
				panic("refusal not accepted")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disp := NewDispatcher(NewMockPublisher(nil), time.Hour)
			disp.registry.UpdateHeartbeat(capWorker("w", 8, 30720))
			disp.EnqueueJob(capJob("big", 8, 20480))
			if _, j, _ := disp.DispatchNext(context.Background()); j == nil {
				t.Fatal("big job not placed")
			}
			tt.end(disp)
			disp.registry.UpdateHeartbeat(capWorker("w", 8, 30720))
			disp.EnqueueJob(capJob("next", 8, 20480))
			if _, j, _ := disp.DispatchNext(context.Background()); j == nil {
				t.Fatal("capacity leaked: the next job is not placed")
			}
		})
	}
}

func TestRequeueRefusedPutsJobBack(t *testing.T) {
	disp := NewDispatcher(NewMockPublisher(nil), time.Hour)
	disp.registry.UpdateHeartbeat(capWorker("w", 8, 30720))
	disp.EnqueueJob(capJob("big", 2, 2048))
	_, _, _ = disp.DispatchNext(context.Background())
	if disp.RequeueRefused("big", "other") {
		t.Fatal("refusal from another worker accepted")
	}
	if !disp.RequeueRefused("big", "w") || !disp.Queue().Contains("big") || disp.IsDispatched("big") {
		t.Fatal("refused job is not back in the queue")
	}
	if disp.registry.IsBadHostForJob("big", "w") {
		t.Fatal("a refusal must not mark the worker bad for the job")
	}
}

func TestFullWorkerAfterRestartStaysFull(t *testing.T) {
	disp := NewDispatcher(NewMockPublisher(nil), time.Hour)
	// registry rebuilt from heartbeats only: the worker reports 0 free
	disp.registry.UpdateHeartbeat(capWorker("w", 0, 0, "a", "b"))
	disp.EnqueueJob(capJob("x", 2, 2048))
	if _, j, _ := disp.DispatchNext(context.Background()); j != nil {
		t.Fatal("full worker received a job")
	}
}
