package dispatcher

import (
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type WorkerRecord struct {
	v1.WorkerHeartbeat
	LastSeen          time.Time
	ConsecutiveErrors int
	QuarantineUntil   time.Time
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

func (r *WorkerRegistry) FindBestIdleWorker(job *v1.BuildJob) (*v1.WorkerHeartbeat, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var bestWorker *v1.WorkerHeartbeat
	bestScore := -1.0

	for _, w := range r.workers {
		if !r.isWorkerEligible(w, job, now) {
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

	if w.Status != "IDLE" {
		return false
	}
	if w.Architecture != job.Architecture {
		return false
	}
	if r.badHosts[job.JobID] != nil && r.badHosts[job.JobID][w.WorkerID] {
		return false // badhost: worker previously failed this job
	}
	return matchesConstraints(&w.Hardware, &job.Resources)
}

func matchesConstraints(hw *v1.WorkerHardware, req *v1.ResourceRequirements) bool {
	if req.MemoryMB > 0 && hw.TotalMemoryMB < req.MemoryMB {
		return false
	}
	if req.VCPUs > 0 && hw.TotalVCPUs < req.VCPUs {
		return false
	}
	if req.DiskGB > 0 && hw.AvailableDiskGB < req.DiskGB {
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
