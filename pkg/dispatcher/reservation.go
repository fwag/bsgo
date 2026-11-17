package dispatcher

import (
	"fmt"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// DefaultReserveAfter is how long a job that fits a worker only when it is
// idle waits before the dispatcher holds that worker for it.
const DefaultReserveAfter = 15 * time.Minute

// SetReserveAfter sets the wait after which a starving job reserves a worker.
// Zero disables reservations.
func (d *Dispatcher) SetReserveAfter(after time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reserveAfter = after
}

// ReservedWorker returns the worker held for the queued job, if any.
func (d *Dispatcher) ReservedWorker(jobID string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for wid, jid := range d.reservations {
		if jid == jobID {
			return wid, true
		}
	}
	return "", false
}

// noteEnqueued records when a job first entered the queue; a later call for
// the same job keeps the first time. The record is in-memory: after a server
// restart the queue is rebuilt and waiting restarts.
func (d *Dispatcher) noteEnqueued(jobID string) {
	if _, ok := d.enqueuedAt[jobID]; !ok {
		d.enqueuedAt[jobID] = d.now()
	}
}

// pruneReservations forgets wait times and reservations of jobs that left the
// queue (dispatched, cancelled, superseded) and reservations whose worker can
// no longer take the job. The caller holds d.mu.
func (d *Dispatcher) pruneReservations(queued map[string]*v1.BuildJob) {
	for id := range d.enqueuedAt {
		if queued[id] == nil {
			delete(d.enqueuedAt, id)
		}
	}
	for wid, jid := range d.reservations {
		job := queued[jid]
		switch {
		case job == nil:
			d.releaseReservation(wid, jid, "job left the queue")
		case !d.registry.CanEverFit(wid, job):
			d.releaseReservation(wid, jid, "worker can no longer take the job")
		}
	}
}

func (d *Dispatcher) releaseReservation(workerID, jobID, why string) {
	delete(d.reservations, workerID)
	d.logf("released %s from job %s (%s)", workerID, jobID, why)
}

// updateReservations reserves a worker for each starving big job, in the given
// (priority) order. A job starves when it has waited for reserveAfter and fits
// no unreserved worker on its free resources. Only jobs that are big for the
// worker (more than half of its vCPUs or memory) reserve it, and jobs that fit
// no worker even when idle reserve nothing. The caller holds d.mu.
func (d *Dispatcher) updateReservations(sorted []*v1.BuildJob) {
	if d.reserveAfter <= 0 {
		return
	}
	now := d.now()
	for _, job := range sorted {
		d.noteEnqueued(job.JobID)
		waited := now.Sub(d.enqueuedAt[job.JobID])
		if waited < d.reserveAfter || d.hasReservation(job.JobID) {
			continue
		}
		held := d.reservedWorkers("")
		if w, _ := d.registry.FindBestIdleWorkerExcluding(job, held); w != nil {
			continue // fits right now; dispatched in this pass
		}
		wid := d.registry.FindReservableWorker(job, held)
		if wid == "" {
			continue
		}
		d.reservations[wid] = job.JobID
		d.logf("reserved %s for job %s (%s/%s, waiting %s, needs %d vCPUs)",
			wid, job.JobID, job.Project, job.Package, waited.Round(time.Second), job.Resources.VCPUs)
	}
}

func (d *Dispatcher) hasReservation(jobID string) bool {
	for _, jid := range d.reservations {
		if jid == jobID {
			return true
		}
	}
	return false
}

// reservedWorkers returns the workers held for jobs other than jobID.
func (d *Dispatcher) reservedWorkers(jobID string) map[string]bool {
	if len(d.reservations) == 0 {
		return nil
	}
	held := make(map[string]bool, len(d.reservations))
	for wid, jid := range d.reservations {
		if jid != jobID {
			held[wid] = true
		}
	}
	return held
}

func (d *Dispatcher) releaseJobReservation(jobID string) {
	for wid, jid := range d.reservations {
		if jid == jobID {
			d.releaseReservation(wid, jid, "job dispatched")
		}
	}
}

func (d *Dispatcher) logf(format string, args ...any) {
	fmt.Printf("[%s] [DISPATCHER] %s\n", d.now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
