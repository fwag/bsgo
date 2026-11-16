package dispatcher

import (
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type jobAllocation struct {
	memoryMB int64
	vcpus    int32
}

type WorkerRecord struct {
	v1.WorkerHeartbeat
	LastSeen          time.Time
	ConsecutiveErrors int
	QuarantineUntil   time.Time
	Allocations       map[string]jobAllocation
}

type WorkerRegistry struct {
	mu                   sync.RWMutex
	workers              map[string]*WorkerRecord
	badHosts             map[string]map[string]bool // jobID -> set of failed workerIDs
	ttl                  time.Duration
	maxConsecutiveErrors int
	quarantineDuration   time.Duration
}

func NewWorkerRegistry(ttl time.Duration) *WorkerRegistry {
	return NewWorkerRegistryWithConfig(ttl, 5*time.Minute, 3)
}

func NewWorkerRegistryWithConfig(ttl, quarantineDuration time.Duration, maxConsecutiveErrors int) *WorkerRegistry {
	if maxConsecutiveErrors <= 0 {
		maxConsecutiveErrors = 3
	}
	if quarantineDuration <= 0 {
		quarantineDuration = 5 * time.Minute
	}
	return &WorkerRegistry{
		workers:              make(map[string]*WorkerRecord),
		badHosts:             make(map[string]map[string]bool),
		ttl:                  ttl,
		maxConsecutiveErrors: maxConsecutiveErrors,
		quarantineDuration:   quarantineDuration,
	}
}

// IsHostInfrastructureFailure returns true if the exit code represents a host environment,
// sandbox, or hypervisor failure (codes 2, 3, 4, 9) rather than a package recipe error (code 1) or cancel (137).
func IsHostInfrastructureFailure(exitCode int) bool {
	return exitCode != 0 && exitCode != 1 && exitCode != 137
}

func (r *WorkerRegistry) UpdateHeartbeat(hb v1.WorkerHeartbeat) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[hb.WorkerID]
	if !exists {
		r.workers[hb.WorkerID] = &WorkerRecord{
			WorkerHeartbeat: hb,
			LastSeen:        time.Now(),
		}
		return
	}

	w.WorkerHeartbeat = hb
	w.LastSeen = time.Now()
}

func (r *WorkerRegistry) SetWorkerStatus(workerID, status, activeJob string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[workerID]
	if exists {
		w.Status = status
		w.ActiveJobID = activeJob
		if status == "IDLE" {
			w.Hardware.AvailableMemoryMB = w.Hardware.TotalMemoryMB
			w.Hardware.AvailableVCPUs = w.Hardware.TotalVCPUs
			w.ActiveJobIDs = nil
		}
	}
}

// AllocateJobResources deducts resources from the worker's capacity pool upon dispatch.
func (r *WorkerRegistry) AllocateJobResources(workerID string, job *v1.BuildJob) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[workerID]
	if !exists {
		return
	}

	// If worker did not advertise dynamic available capacity, treat as single-job worker
	if w.Hardware.AvailableMemoryMB <= 0 && w.Hardware.AvailableVCPUs <= 0 && len(w.ActiveJobIDs) == 0 {
		w.Status = "BUSY"
		w.ActiveJobID = job.JobID
		w.ActiveJobIDs = []string{job.JobID}
		return
	}

	reqMem := int64(job.Resources.MemoryMB)
	if reqMem <= 0 {
		reqMem = 2048
	}
	reqVCPUs := int32(job.Resources.VCPUs)
	if reqVCPUs <= 0 {
		reqVCPUs = 1
	}

	w.Hardware.AvailableMemoryMB -= reqMem
	if w.Hardware.AvailableMemoryMB < 0 {
		w.Hardware.AvailableMemoryMB = 0
	}
	w.Hardware.AvailableVCPUs -= reqVCPUs
	if w.Hardware.AvailableVCPUs < 0 {
		w.Hardware.AvailableVCPUs = 0
	}

	if w.Allocations == nil {
		w.Allocations = make(map[string]jobAllocation)
	}
	w.Allocations[job.JobID] = jobAllocation{memoryMB: reqMem, vcpus: reqVCPUs}

	w.ActiveJobID = job.JobID
	w.ActiveJobIDs = append(w.ActiveJobIDs, job.JobID)

	if w.Hardware.AvailableMemoryMB < 1024 || w.Hardware.AvailableVCPUs < 1 {
		w.Status = "BUSY"
	} else {
		w.Status = "IDLE"
	}
}

// ReleaseJobResources restores resources back to the worker's capacity pool upon completion.
func (r *WorkerRegistry) ReleaseJobResources(workerID, jobID string, req *v1.ResourceRequirements) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[workerID]
	if !exists {
		return
	}

	var reqMem int64
	var reqVCPUs int32
	if alloc, ok := w.Allocations[jobID]; ok {
		reqMem = alloc.memoryMB
		reqVCPUs = alloc.vcpus
		delete(w.Allocations, jobID)
	} else if req != nil {
		reqMem = int64(req.MemoryMB)
		reqVCPUs = int32(req.VCPUs)
	} else {
		reqMem = 2048
		reqVCPUs = 1
	}

	w.Hardware.AvailableMemoryMB += reqMem
	if w.Hardware.AvailableMemoryMB > w.Hardware.TotalMemoryMB {
		w.Hardware.AvailableMemoryMB = w.Hardware.TotalMemoryMB
	}
	w.Hardware.AvailableVCPUs += reqVCPUs
	if w.Hardware.AvailableVCPUs > w.Hardware.TotalVCPUs {
		w.Hardware.AvailableVCPUs = w.Hardware.TotalVCPUs
	}

	var remaining []string
	for _, id := range w.ActiveJobIDs {
		if id != jobID {
			remaining = append(remaining, id)
		}
	}
	w.ActiveJobIDs = remaining
	if len(remaining) > 0 {
		w.ActiveJobID = remaining[len(remaining)-1]
	} else {
		w.ActiveJobID = ""
	}

	if w.Hardware.AvailableMemoryMB >= 1024 && w.Hardware.AvailableVCPUs >= 1 {
		w.Status = "IDLE"
	} else {
		w.Status = "BUSY"
	}
}

// RecordJobFailure logs an execution failure against the worker. If it is an infrastructure
// failure, it adds the worker to the job's badhost set and increments the worker's consecutive error count.
func (r *WorkerRegistry) RecordJobFailure(jobID, workerID string, exitCode int, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !IsHostInfrastructureFailure(exitCode) {
		return
	}

	if _, ok := r.badHosts[jobID]; !ok {
		r.badHosts[jobID] = make(map[string]bool)
	}
	r.badHosts[jobID][workerID] = true

	w, exists := r.workers[workerID]
	if !exists {
		return
	}

	w.ConsecutiveErrors++
	if w.ConsecutiveErrors >= r.maxConsecutiveErrors {
		w.Status = "QUARANTINE"
		w.QuarantineUntil = now.Add(r.quarantineDuration)
	}
}

// RecordJobSuccess resets a worker's consecutive error counter upon successful job completion.
func (r *WorkerRegistry) RecordJobSuccess(workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if w, exists := r.workers[workerID]; exists {
		w.ConsecutiveErrors = 0
		if w.Status == "QUARANTINE" {
			w.Status = "IDLE"
		}
	}
}

func (r *WorkerRegistry) IsBadHostForJob(jobID, workerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.badHosts[jobID] != nil && r.badHosts[jobID][workerID]
}

func (r *WorkerRegistry) IsJobHostExhausted(jobID string, threshold int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if threshold <= 0 {
		threshold = 3
	}
	return len(r.badHosts[jobID]) >= threshold
}

// FindStaleOrResetWorkers identifies dead workers (heartbeat timeout) and reset workers (restarted and reported 0 active jobs while having allocations).
func (r *WorkerRegistry) FindStaleOrResetWorkers(now time.Time) (deadWorkers, idleResetWorkers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for wid, w := range r.workers {
		if now.Sub(w.LastSeen) > r.ttl {
			deadWorkers = append(deadWorkers, wid)
			continue
		}
		if len(w.ActiveJobIDs) == 0 && w.ActiveJobID == "" && len(w.Allocations) > 0 {
			idleResetWorkers = append(idleResetWorkers, wid)
		}
	}
	return deadWorkers, idleResetWorkers
}

func (r *WorkerRegistry) FindBestIdleWorker(job *v1.BuildJob) (*v1.WorkerHeartbeat, float64) {
	return r.FindBestIdleWorkerExcluding(job, nil)
}

// FindBestIdleWorkerExcluding is FindBestIdleWorker that never returns a
// worker in the excluded set (workers held for a reserved job).
func (r *WorkerRegistry) FindBestIdleWorkerExcluding(job *v1.BuildJob, excluded map[string]bool) (*v1.WorkerHeartbeat, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var bestWorker *v1.WorkerHeartbeat
	bestScore := -1.0

	for _, w := range r.workers {
		if excluded[w.WorkerID] || !r.isWorkerEligible(w, job, now) {
			continue
		}

		score := calculateCacheScore(w.CachedRPMHashes, job.ChrootRPMs)
		if score > bestScore {
			bestScore = score
			hbCopy := w.WorkerHeartbeat
			bestWorker = &hbCopy
		}
	}

	return bestWorker, bestScore
}

func (r *WorkerRegistry) isWorkerEligible(w *WorkerRecord, job *v1.BuildJob, now time.Time) bool {
	if !r.isWorkerUsable(w, job, now) {
		return false
	}
	if w.Status == "BUSY" && (w.Hardware.AvailableMemoryMB < 1024 || w.Hardware.AvailableVCPUs < 1) {
		return false
	}
	return matchesConstraints(&w.Hardware, &job.Resources)
}

// isWorkerUsable checks everything but free resources: liveness, health,
// architecture, badhost and sandbox constraints.
func (r *WorkerRegistry) isWorkerUsable(w *WorkerRecord, job *v1.BuildJob, now time.Time) bool {
	if now.Sub(w.LastSeen) > r.ttl {
		return false // Dead or timed out worker
	}

	// Check and handle quarantine expiration
	if w.Status == "QUARANTINE" {
		if now.Before(w.QuarantineUntil) {
			return false // Still in quarantine
		}
		// Quarantine expired: recover to IDLE
		w.Status = "IDLE"
		w.ConsecutiveErrors = 0
	}

	if w.Status == "DRAINING" || w.Status == "BADHOST" {
		return false
	}
	if !w.CanBuild(job.Architecture) {
		return false
	}
	if r.badHosts[job.JobID] != nil && r.badHosts[job.JobID][w.WorkerID] {
		return false // badhost: worker previously failed this job
	}
	return acceptsSandbox(&w.WorkerHeartbeat, job.Constraints)
}

// fitsWhenIdle reports whether the job would fit the worker with all of its
// resources free.
func (r *WorkerRegistry) fitsWhenIdle(w *WorkerRecord, job *v1.BuildJob, now time.Time) bool {
	if !r.isWorkerUsable(w, job, now) {
		return false
	}
	hw := w.Hardware
	hw.AvailableMemoryMB = hw.TotalMemoryMB
	hw.AvailableVCPUs = hw.TotalVCPUs
	return matchesConstraints(&hw, &job.Resources)
}

// CanEverFit reports whether the job fits the worker by capacity, that is
// when the worker is idle.
func (r *WorkerRegistry) CanEverFit(workerID string, job *v1.BuildJob) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[workerID]
	return ok && r.fitsWhenIdle(w, job, time.Now())
}

// FindReservableWorker picks the worker to hold for a job that does not fit
// the free resources anywhere: among the workers the job fits when idle (and
// that are not excluded) the one running the fewest jobs, then the one with
// the most free vCPUs, then the lowest ID. It returns "" when none exists.
func (r *WorkerRegistry) FindReservableWorker(job *v1.BuildJob, excluded map[string]bool) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var best *WorkerRecord
	for _, w := range r.workers {
		if excluded[w.WorkerID] || !r.fitsWhenIdle(w, job, now) {
			continue
		}
		if best == nil || reservePreferred(w, best) {
			best = w
		}
	}
	if best == nil {
		return ""
	}
	return best.WorkerID
}

func reservePreferred(a, b *WorkerRecord) bool {
	if len(a.ActiveJobIDs) != len(b.ActiveJobIDs) {
		return len(a.ActiveJobIDs) < len(b.ActiveJobIDs)
	}
	if a.Hardware.AvailableVCPUs != b.Hardware.AvailableVCPUs {
		return a.Hardware.AvailableVCPUs > b.Hardware.AvailableVCPUs
	}
	return a.WorkerID < b.WorkerID
}

// acceptsSandbox checks the sandbox constraints of a job against the sandbox
// the worker reports. A worker that only takes constrained jobs
// (sandbox.accept_unconstrained: false) refuses jobs without a sandbox
// constraint, so a KVM worker is kept for the jobs that need it.
func acceptsSandbox(w *v1.WorkerHeartbeat, c *v1.JobConstraints) bool {
	if !c.SandboxConstrained() {
		return !w.ConstrainedOnly
	}
	return c.SandboxAllows(w.Hardware.SandboxType)
}

func matchesConstraints(hw *v1.WorkerHardware, req *v1.ResourceRequirements) bool {
	availMem := hw.AvailableMemoryMB
	if availMem <= 0 && hw.TotalMemoryMB > 0 {
		availMem = hw.TotalMemoryMB
	}
	if req.MemoryMB > 0 && availMem < int64(req.MemoryMB) {
		return false
	}

	availVCPUs := hw.AvailableVCPUs
	if availVCPUs <= 0 && hw.TotalVCPUs > 0 {
		availVCPUs = hw.TotalVCPUs
	}
	if req.VCPUs > 0 && availVCPUs < int32(req.VCPUs) {
		return false
	}

	if req.DiskGB > 0 && hw.AvailableDiskGB < int64(req.DiskGB) {
		return false
	}
	if req.SandboxType != "" && hw.SandboxType != "" && req.SandboxType != hw.SandboxType {
		return false
	}
	return true
}

func calculateCacheScore(cachedHashes []string, neededRPMs []v1.ChrootRPM) float64 {
	if len(neededRPMs) == 0 {
		return 0.0
	}

	cacheMap := make(map[string]struct{}, len(cachedHashes))
	for _, h := range cachedHashes {
		cacheMap[h] = struct{}{}
	}

	matched := 0
	for _, rpm := range neededRPMs {
		if _, ok := cacheMap[rpm.SHA256]; ok {
			matched++
		}
	}

	return float64(matched) / float64(len(neededRPMs))
}
