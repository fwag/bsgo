package dispatcher

import (
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type WorkerRecord struct {
	v1.WorkerHeartbeat
	LastSeen time.Time
}

type WorkerRegistry struct {
	mu      sync.RWMutex
	workers map[string]*WorkerRecord
	ttl     time.Duration
}

func NewWorkerRegistry(ttl time.Duration) *WorkerRegistry {
	return &WorkerRegistry{
		workers: make(map[string]*WorkerRecord),
		ttl:     ttl,
	}
}

func (r *WorkerRegistry) UpdateHeartbeat(hb v1.WorkerHeartbeat) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.workers[hb.WorkerID] = &WorkerRecord{
		WorkerHeartbeat: hb,
		LastSeen:        time.Now(),
	}
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

func (r *WorkerRegistry) FindBestIdleWorker(job *v1.BuildJob) (*v1.WorkerHeartbeat, float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := time.Now()
	var bestWorker *v1.WorkerHeartbeat
	bestScore := -1.0

	for _, w := range r.workers {
		if now.Sub(w.LastSeen) > r.ttl {
			continue // Dead or timed out worker
		}
		if w.Status != "IDLE" {
			continue // Busy worker
		}
		if w.Architecture != job.Architecture {
			continue // Architecture mismatch
		}
		if !matchesConstraints(&w.Hardware, &job.Resources) {
			continue // Insufficient RAM, CPU, or disk
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
