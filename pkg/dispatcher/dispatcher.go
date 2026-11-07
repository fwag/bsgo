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
	mu           sync.Mutex
	registry     *WorkerRegistry
	queue        *PriorityQueue
	fairShare    *FairShareManager
	publisher    Publisher
	activeBuilds map[string]string // key: "prj:pkg:tgt:arch" -> jobID
	abortHandler func(jobID, reason string)
}

func NewDispatcher(pub Publisher, workerTTL time.Duration) *Dispatcher {
	return &Dispatcher{
		registry:     NewWorkerRegistry(workerTTL),
		queue:        NewPriorityQueue(),
		fairShare:    NewFairShareManager(30*time.Minute, 0.05, 100.0, 0.01),
		publisher:    pub,
		activeBuilds: make(map[string]string),
	}
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

	buildKey := fmt.Sprintf("%s:%s:%s:%s", job.Project, job.Package, job.Target, job.Architecture)
	if oldJobID, exists := d.activeBuilds[buildKey]; exists {
		d.signalAbort(oldJobID, "SUPERSEDED_BY_NEW_COMMIT")
	}

	d.queue.Push(job)
}

func (d *Dispatcher) DispatchNext(_ context.Context) (string, *v1.BuildJob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	snapshot := d.queue.Snapshot()
	d.fairShare.SortJobsFairShare(snapshot, time.Now())

	for _, job := range snapshot {
		worker, _ := d.registry.FindBestIdleWorker(job)
		if worker != nil {
			poppedJob := d.queue.RemoveJob(job.JobID)
			if poppedJob == nil {
				continue
			}
			d.registry.SetWorkerStatus(worker.WorkerID, "BUSY", poppedJob.JobID)

			buildKey := fmt.Sprintf("%s:%s:%s:%s", poppedJob.Project, poppedJob.Package, poppedJob.Target, poppedJob.Architecture)
			d.activeBuilds[buildKey] = poppedJob.JobID

			if err := d.sendJobToWorker(worker.WorkerID, poppedJob); err != nil {
				return "", nil, err
			}

			return worker.WorkerID, poppedJob, nil
		}
	}

	return "", nil, nil // No idle matching worker available
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
