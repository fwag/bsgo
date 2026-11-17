package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type Dispatcher struct {
	mu             sync.Mutex
	registry       *WorkerRegistry
	queue          *PriorityQueue
	fairShare      *FairShareManager
	publisher      Publisher
	activeBuilds   map[string]string       // key: "prj:pkg:tgt:arch" -> jobID
	dispatchedJobs map[string]*v1.BuildJob // key: jobID -> BuildJob
	jobWorkers     map[string]string       // key: jobID -> workerID
	// running maps job IDs that workers report as active (heartbeats) to the
	// worker. After a server restart it is the only record of builds that
	// are still running, so their jobs are not dispatched a second time.
	running      map[string]string
	reportedAt   map[string]time.Time // workerID -> time of its last report
	// dispatchedAt is when each dispatched job was sent to its worker.
	dispatchedAt map[string]time.Time
	// dispatchGrace is how long a freshly dispatched job may be missing from
	// the worker's reports: the job message and the heartbeats travel
	// independently, so a heartbeat sent just before the job arrived says
	// nothing about it.
	dispatchGrace time.Duration
	workerTTL    time.Duration
	abortHandler func(jobID, reason string)

	// reservations maps a worker held for a starving job to that job.
	reservations map[string]string
	// enqueuedAt is when each queued job first entered the queue.
	enqueuedAt   map[string]time.Time
	reserveAfter time.Duration
	now          func() time.Time
}

func NewDispatcher(pub Publisher, workerTTL time.Duration) *Dispatcher {
	return &Dispatcher{
		registry:       NewWorkerRegistry(workerTTL),
		queue:          NewPriorityQueue(),
		fairShare:      NewFairShareManager(30*time.Minute, 0.05, 100.0, 0.01),
		publisher:      pub,
		activeBuilds:   make(map[string]string),
		dispatchedJobs: make(map[string]*v1.BuildJob),
		jobWorkers:     make(map[string]string),
		running:        make(map[string]string),
		reportedAt:     make(map[string]time.Time),
		dispatchedAt:   make(map[string]time.Time),
		dispatchGrace:  2 * workerTTL,
		workerTTL:      workerTTL,
		reservations:   make(map[string]string),
		enqueuedAt:     make(map[string]time.Time),
		reserveAfter:   DefaultReserveAfter,
		now:            time.Now,
	}
}

// ObserveActiveJobs records the jobs a worker reports as active in its
// heartbeat, replacing that worker's previous report.
func (d *Dispatcher) ObserveActiveJobs(workerID string, jobIDs []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, w := range d.running {
		if w == workerID {
			delete(d.running, id)
		}
	}
	for _, id := range jobIDs {
		d.running[id] = workerID
	}
	d.reportedAt[workerID] = time.Now()
}

// IsDispatched reports whether the job was dispatched by this dispatcher and
// has not finished or been reaped.
func (d *Dispatcher) IsDispatched(jobID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.dispatchedJobs[jobID]
	return ok
}

// IsRunning reports whether a worker currently reports the job as active.
func (d *Dispatcher) IsRunning(jobID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runningLocked(jobID)
}

func (d *Dispatcher) runningLocked(jobID string) bool {
	w, ok := d.running[jobID]
	return ok && time.Since(d.reportedAt[w]) <= 2*d.workerTTL
}

func (d *Dispatcher) Registry() *WorkerRegistry {
	return d.registry
}

func (d *Dispatcher) Queue() *PriorityQueue {
	return d.queue
}

func (d *Dispatcher) FairShare() *FairShareManager {
	return d.fairShare
}

func (d *Dispatcher) RecordJobCompletion(project string, duration time.Duration) {
	d.fairShare.RecordBuildTime(project, duration, time.Now())
}

// RecordJobResult updates worker health and badhost tracking, and records compute duration.
func (d *Dispatcher) RecordJobResult(jobID, workerID, project string, exitCode int, duration time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for k, v := range d.activeBuilds {
		if v == jobID {
			delete(d.activeBuilds, k)
			break
		}
	}
	delete(d.dispatchedJobs, jobID)
	delete(d.jobWorkers, jobID)
	delete(d.dispatchedAt, jobID)

	d.registry.ReleaseJobResources(workerID, jobID, nil)

	now := time.Now()
	if exitCode == 0 {
		d.registry.RecordJobSuccess(workerID)
		d.fairShare.RecordBuildTime(project, duration, now)
		return
	}

	d.registry.RecordJobFailure(jobID, workerID, exitCode, now)
	if exitCode == 1 {
		// Package build failure (exit 1): still record CPU compilation time
		d.fairShare.RecordBuildTime(project, duration, now)
	}
}

func (d *Dispatcher) OnAbort(fn func(jobID, reason string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.abortHandler = fn
}

func (d *Dispatcher) EnqueueJob(job *v1.BuildJob) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Idempotent per job ID: the checker re-emits scheduled jobs on every
	// pass to rebuild the queue after a restart.
	if _, dispatched := d.dispatchedJobs[job.JobID]; dispatched || d.queue.Contains(job.JobID) {
		return
	}
	if _, running := d.running[job.JobID]; running {
		return
	}
	buildKey := fmt.Sprintf("%s:%s:%s:%s", job.Project, job.Package, job.Target, job.Architecture)
	if oldJobID, exists := d.activeBuilds[buildKey]; exists {
		d.signalAbort(oldJobID, "SUPERSEDED_BY_NEW_COMMIT")
	}

	d.queue.RemoveByBuildKey(job.Project, job.Package, job.Target, job.Architecture)
	d.queue.Push(job)
	d.noteEnqueued(job.JobID)
}

func (d *Dispatcher) DispatchNext(_ context.Context) (string, *v1.BuildJob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	snapshot := d.queue.Snapshot()
	d.fairShare.SortJobsFairShare(snapshot, time.Now())

	queued := make(map[string]*v1.BuildJob, len(snapshot))
	for _, job := range snapshot {
		queued[job.JobID] = job
	}
	d.pruneReservations(queued)
	d.updateReservations(snapshot)

	for _, job := range snapshot {
		if d.runningLocked(job.JobID) {
			// A worker still runs this job; sending it again would make the
			// worker kill or duplicate the build.
			d.queue.RemoveJob(job.JobID)
			continue
		}
		worker, _ := d.registry.FindBestIdleWorkerExcluding(job, d.reservedWorkers(job.JobID))
		if worker != nil {
			poppedJob := d.queue.RemoveJob(job.JobID)
			if poppedJob == nil {
				continue
			}
			d.releaseJobReservation(poppedJob.JobID)
			d.registry.AllocateJobResources(worker.WorkerID, poppedJob)

			buildKey := fmt.Sprintf("%s:%s:%s:%s", poppedJob.Project, poppedJob.Package, poppedJob.Target, poppedJob.Architecture)
			d.activeBuilds[buildKey] = poppedJob.JobID
			d.dispatchedJobs[poppedJob.JobID] = poppedJob
			d.jobWorkers[poppedJob.JobID] = worker.WorkerID
			d.dispatchedAt[poppedJob.JobID] = time.Now()

			if err := d.sendJobToWorker(worker.WorkerID, poppedJob); err != nil {
				delete(d.activeBuilds, buildKey)
				delete(d.dispatchedJobs, poppedJob.JobID)
				delete(d.jobWorkers, poppedJob.JobID)
				delete(d.dispatchedAt, poppedJob.JobID)
				d.registry.ReleaseJobResources(worker.WorkerID, poppedJob.JobID, &poppedJob.Resources)
				return "", nil, err
			}

			return worker.WorkerID, poppedJob, nil
		}
	}

	return "", nil, nil // No idle matching worker available
}

// SetDispatchGrace sets how long a freshly dispatched job is exempt from the
// restart check of ReapStaleJobs.
func (d *Dispatcher) SetDispatchGrace(grace time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dispatchGrace = grace
}

// staleJobWorkers returns the workers whose jobs are candidates for reaping:
// those silent for longer than the TTL and those that restarted.
func (d *Dispatcher) staleJobWorkers(now time.Time) (dead, restarted map[string]bool) {
	deadIDs, restartedIDs := d.registry.FindStaleOrResetWorkers(now)
	dead, restarted = make(map[string]bool), make(map[string]bool)
	for _, wid := range deadIDs {
		dead[wid] = true
	}
	for _, wid := range restartedIDs {
		restarted[wid] = true
	}
	return dead, restarted
}

// worthReaping decides whether the job of a stale worker is really lost.
// A worker that is merely slow to heartbeat (a loaded host) keeps a job it
// reported as running until it was silent for three TTLs, and a job sent only
// moments ago is not lost because a heartbeat predates it.
func (d *Dispatcher) worthReaping(jobID, workerID string, dead bool, now time.Time) bool {
	if dead {
		if d.running[jobID] == workerID && now.Sub(d.reportedAt[workerID]) <= 3*d.workerTTL {
			return false
		}
		return true
	}
	if _, running := d.running[jobID]; running {
		return false
	}
	return now.Sub(d.dispatchedAt[jobID]) > d.dispatchGrace
}

// ReapStaleJobs checks for dead workers or restarted workers, aborts unacknowledged jobs, and re-enqueues them.
func (d *Dispatcher) ReapStaleJobs(now time.Time) []*v1.BuildJob {
	d.mu.Lock()
	defer d.mu.Unlock()

	dead, restarted := d.staleJobWorkers(now)
	if len(dead) == 0 && len(restarted) == 0 {
		return nil
	}

	var reaped []*v1.BuildJob
	for jobID, wid := range d.jobWorkers {
		if !dead[wid] && !restarted[wid] {
			continue
		}
		job := d.dispatchedJobs[jobID]
		if job == nil || !d.worthReaping(jobID, wid, dead[wid], now) {
			continue
		}

		delete(d.dispatchedJobs, jobID)
		delete(d.jobWorkers, jobID)
		delete(d.dispatchedAt, jobID)
		buildKey := fmt.Sprintf("%s:%s:%s:%s", job.Project, job.Package, job.Target, job.Architecture)
		delete(d.activeBuilds, buildKey)

		d.registry.ReleaseJobResources(wid, jobID, nil)

		d.signalAbort(jobID, "WORKER_LEASE_EXPIRED")
		d.queue.Push(job)
		reaped = append(reaped, job)
	}

	return reaped
}

func (d *Dispatcher) sendJobToWorker(workerID string, job *v1.BuildJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	subject := fmt.Sprintf("bsgo.workers.%s.job", workerID)
	return d.publisher.Publish(subject, data)
}

func (d *Dispatcher) signalAbort(jobID, reason string) {
	if d.abortHandler != nil {
		d.abortHandler(jobID, reason)
	}

	ctrl := v1.JobControl{
		JobID:         jobID,
		Action:        "ABORT",
		Reason:        reason,
		TimestampUnix: time.Now().Unix(),
	}
	data, _ := json.Marshal(ctrl)
	subject := fmt.Sprintf("bsgo.jobs.%s.control", jobID)
	_ = d.publisher.Publish(subject, data)
}
