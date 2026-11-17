package dispatcher

import (
	"context"
	"testing"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// busyWorker is a worker of the given size with freeVCPUs left; it runs a
// job when not all vCPUs are free.
func busyWorker(id string, totalVCPUs, freeVCPUs int32, constrainedOnly bool) v1.WorkerHeartbeat {
	var active []string
	if freeVCPUs < totalVCPUs {
		active = []string{"running-" + id}
	}
	return v1.WorkerHeartbeat{
		WorkerID: id, Status: "IDLE", Architecture: "x86_64", ConstrainedOnly: constrainedOnly,
		ActiveJobIDs: active,
		Hardware: v1.WorkerHardware{
			TotalVCPUs: totalVCPUs, AvailableVCPUs: freeVCPUs,
			TotalMemoryMB: 32768, AvailableMemoryMB: 32768,
			SandboxType: "nspawn",
		},
		TimestampUnix: time.Now().Unix(),
	}
}

func kvmWorker(id string, totalVCPUs, freeVCPUs int32) v1.WorkerHeartbeat {
	w := busyWorker(id, totalVCPUs, freeVCPUs, true)
	w.Hardware.SandboxType = "kvm"
	return w
}

func resJob(id string, vcpus, prio int32) *v1.BuildJob {
	return &v1.BuildJob{
		JobID: id, Project: "p", Package: id, Target: "t", Architecture: "x86_64",
		Priority: prio, Resources: v1.ResourceRequirements{VCPUs: vcpus, MemoryMB: 1024},
	}
}

func newReservationDispatcher(clock *fakeClock, reserveAfter time.Duration, workers ...v1.WorkerHeartbeat) *Dispatcher {
	d := NewDispatcher(NewMockPublisher(nil), time.Hour)
	d.now = clock.now
	d.SetReserveAfter(reserveAfter)
	for _, w := range workers {
		d.Registry().UpdateHeartbeat(w)
	}
	return d
}

func TestReservationDecision(t *testing.T) {
	const threshold = 15 * time.Minute
	kvm := &v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "kvm"}}}
	tests := []struct {
		name         string
		workers      []v1.WorkerHeartbeat
		reserveAfter time.Duration
		wait         time.Duration
		bigVCPUs     int32
		constraints  *v1.JobConstraints
		want         string
	}{
		{"waited past threshold", []v1.WorkerHeartbeat{busyWorker("w1", 8, 2, false)}, threshold, threshold, 8, nil, "w1"},
		{"not waited long enough", []v1.WorkerHeartbeat{busyWorker("w1", 8, 2, false)}, threshold, threshold - time.Second, 8, nil, ""},
		{"threshold 0 disables", []v1.WorkerHeartbeat{busyWorker("w1", 8, 2, false)}, 0, 48 * time.Hour, 8, nil, ""},
		{"fits no worker by capacity", []v1.WorkerHeartbeat{busyWorker("w1", 8, 2, false)}, threshold, time.Hour, 16, nil, ""},
		{"fits free resources", []v1.WorkerHeartbeat{busyWorker("w1", 8, 8, false)}, threshold, time.Hour, 4, nil, ""},
		{"small job never reserves a busy worker", []v1.WorkerHeartbeat{busyWorker("w1", 8, 1, false)}, threshold, 48 * time.Hour, 4, nil, ""},
		{"job above half of vCPUs reserves", []v1.WorkerHeartbeat{busyWorker("w1", 8, 1, false)}, threshold, threshold, 5, nil, "w1"},
		{"idle worker preferred", []v1.WorkerHeartbeat{busyWorker("w1", 8, 2, false), busyWorker("w2", 8, 3, false)}, threshold, threshold, 8, nil, "w2"},
		{"constrained-only worker not reserved for unconstrained job", []v1.WorkerHeartbeat{kvmWorker("kvm", 8, 2)}, threshold, time.Hour, 8, nil, ""},
		{"constrained-only worker reserved for kvm job", []v1.WorkerHeartbeat{kvmWorker("kvm", 8, 2)}, threshold, time.Hour, 8, kvm, "kvm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			d := newReservationDispatcher(clock, tt.reserveAfter, tt.workers...)
			big := resJob("big", tt.bigVCPUs, 1)
			big.Constraints = tt.constraints
			d.EnqueueJob(big)
			clock.advance(tt.wait)
			_, _, _ = d.DispatchNext(context.Background())
			if got, _ := d.ReservedWorker("big"); got != tt.want {
				t.Errorf("reserved worker %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReservationHoldsWorkerAndReleasesOnDispatch(t *testing.T) {
	clock := newFakeClock()
	d := newReservationDispatcher(clock, 15*time.Minute, busyWorker("w1", 8, 2, false))
	d.EnqueueJob(resJob("big", 8, 1))
	clock.advance(20 * time.Minute)
	d.EnqueueJob(resJob("small", 1, 5)) // higher priority, fits the free vCPUs

	if _, job, _ := d.DispatchNext(context.Background()); job != nil {
		t.Fatalf("dispatched %s to the reserved worker", job.JobID)
	}
	if w, ok := d.ReservedWorker("big"); !ok || w != "w1" {
		t.Fatalf("reserved %q %v, want w1", w, ok)
	}

	// The running job ends: the worker is idle and the reserved job fits.
	d.Registry().UpdateHeartbeat(busyWorker("w1", 8, 8, false))
	w, job, _ := d.DispatchNext(context.Background())
	if job == nil || job.JobID != "big" || w != "w1" {
		t.Fatalf("dispatched %v on %q, want big on w1", job, w)
	}
	if len(d.reservations) != 0 {
		t.Errorf("reservation kept after dispatch: %v", d.reservations)
	}
}

func TestReservationOtherWorkerBackfills(t *testing.T) {
	clock := newFakeClock()
	d := newReservationDispatcher(clock, 15*time.Minute,
		busyWorker("w1", 8, 2, false), busyWorker("w2", 4, 4, false))
	d.EnqueueJob(resJob("big", 8, 1))
	clock.advance(20 * time.Minute)
	d.EnqueueJob(resJob("small", 1, 5))

	w, job, _ := d.DispatchNext(context.Background())
	if job == nil || job.JobID != "small" || w != "w2" {
		t.Fatalf("dispatched %v on %q, want small on w2", job, w)
	}
	if got, _ := d.ReservedWorker("big"); got != "w1" {
		t.Errorf("reserved %q, want w1", got)
	}
}

func TestReservationReleasedWhenJobLeavesQueue(t *testing.T) {
	supersede := &v1.BuildJob{JobID: "big2", Project: "p", Package: "big", Target: "t", Architecture: "x86_64"}
	tests := []struct {
		name   string
		remove func(d *Dispatcher)
	}{
		{"RemoveJob", func(d *Dispatcher) { d.Queue().RemoveJob("big") }},
		{"RemoveByBuildKey", func(d *Dispatcher) { d.Queue().RemoveByBuildKey("p", "big", "t", "x86_64") }},
		{"superseded", func(d *Dispatcher) { d.EnqueueJob(supersede) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			d := newReservationDispatcher(clock, 15*time.Minute, busyWorker("w1", 8, 2, false))
			d.EnqueueJob(resJob("big", 8, 1))
			clock.advance(20 * time.Minute)
			_, _, _ = d.DispatchNext(context.Background())
			if _, ok := d.ReservedWorker("big"); !ok {
				t.Fatal("no reservation made")
			}
			tt.remove(d)
			_, _, _ = d.DispatchNext(context.Background())
			if len(d.reservations) != 0 {
				t.Errorf("reservations left: %v", d.reservations)
			}
			if _, ok := d.enqueuedAt["big"]; ok {
				t.Error("wait time of a removed job kept")
			}
		})
	}
}

func TestReservationMovesWhenWorkerBecomesIneligible(t *testing.T) {
	clock := newFakeClock()
	d := newReservationDispatcher(clock, 15*time.Minute, busyWorker("w1", 8, 2, false), busyWorker("w2", 8, 3, false))
	d.EnqueueJob(resJob("big", 8, 1))
	clock.advance(20 * time.Minute)
	_, _, _ = d.DispatchNext(context.Background())
	if got, _ := d.ReservedWorker("big"); got != "w2" {
		t.Fatalf("reserved %q, want w2 (most free vCPUs)", got)
	}

	drain := busyWorker("w2", 8, 3, false)
	drain.Status = "DRAINING"
	d.Registry().UpdateHeartbeat(drain)
	_, _, _ = d.DispatchNext(context.Background())
	if got, _ := d.ReservedWorker("big"); got != "w1" {
		t.Errorf("reserved %q after w2 drained, want w1", got)
	}
}

func TestOneReservationPerWorkerInPriorityOrder(t *testing.T) {
	clock := newFakeClock()
	d := newReservationDispatcher(clock, 15*time.Minute, busyWorker("w1", 8, 2, false))
	d.EnqueueJob(resJob("low", 8, 1))
	d.EnqueueJob(resJob("high", 8, 9))
	clock.advance(20 * time.Minute)
	_, _, _ = d.DispatchNext(context.Background())
	if got, _ := d.ReservedWorker("high"); got != "w1" {
		t.Errorf("high priority job reserved %q, want w1", got)
	}
	if got, ok := d.ReservedWorker("low"); ok {
		t.Errorf("second job also reserved %q", got)
	}
}

func TestReenqueueKeepsWaitTime(t *testing.T) {
	clock := newFakeClock()
	d := newReservationDispatcher(clock, 15*time.Minute, busyWorker("w1", 8, 2, false))
	job := resJob("big", 8, 1)
	d.EnqueueJob(job)
	clock.advance(10 * time.Minute)
	d.EnqueueJob(job) // the checker re-emits the queued job
	clock.advance(6 * time.Minute)
	_, _, _ = d.DispatchNext(context.Background())
	if _, ok := d.ReservedWorker("big"); !ok {
		t.Error("re-enqueue reset the wait time")
	}
}

func TestSmallJobBackfillsAndIsHeldOffReservedWorker(t *testing.T) {
	clock := newFakeClock()
	d := newReservationDispatcher(clock, 15*time.Minute, busyWorker("w1", 8, 2, false))
	d.EnqueueJob(resJob("big", 8, 1))
	d.EnqueueJob(resJob("small", 2, 5))
	clock.advance(time.Hour)

	// small waited long too but never reserves; big holds the only worker.
	if _, job, _ := d.DispatchNext(context.Background()); job != nil {
		t.Fatalf("dispatched %s to the reserved worker", job.JobID)
	}
	if got, _ := d.ReservedWorker("big"); got != "w1" {
		t.Errorf("big reserved %q, want w1", got)
	}
	if got, ok := d.ReservedWorker("small"); ok {
		t.Errorf("small job reserved %q", got)
	}

	// A second, unreserved worker takes the small job.
	d.Registry().UpdateHeartbeat(busyWorker("w2", 8, 8, false))
	w, job, _ := d.DispatchNext(context.Background())
	if job == nil || job.JobID != "small" || w != "w2" {
		t.Fatalf("dispatched %v on %q, want small on w2", job, w)
	}
}
