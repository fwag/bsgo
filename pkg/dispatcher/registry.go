package dispatcher

import (
	"fmt"
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// pendingAckTimeout bounds how long a dispatched job keeps its resources
// reserved on the server while no heartbeat of the worker lists it. After
// that the worker is taken to have dropped the job (the watchdog requeues it).
const pendingAckTimeout = 2 * time.Minute

// jobAllocation is the capacity a dispatched job claims on a worker. It is
// pending until a heartbeat of the worker lists the job: before that the
// availability the heartbeat reports does not yet include the job.
type jobAllocation struct {
	memoryMB     int64
	vcpus        int32
	dispatched   time.Time
	acknowledged bool
}

type WorkerRecord struct {
	v1.WorkerHeartbeat
	LastSeen          time.Time
	ConsecutiveErrors int
	QuarantineUntil   time.Time
	Allocations       map[string]jobAllocation
	// noAvailability marks a worker whose last heartbeat lists no job and
	// reports no free capacity: it does not advertise availability and runs
	// one job at a time.
	noAvailability bool
	// silenceLogged and statusLogged mark that the current silence was
	// already reported, so it is logged once rather than on every pass.
	silenceLogged bool
	statusLogged  bool
}

type WorkerRegistry struct {
	mu                   sync.RWMutex
	workers              map[string]*WorkerRecord
	badHosts             map[string]map[string]bool // jobID -> set of failed workerIDs
	ttl                  time.Duration
	maxConsecutiveErrors int
	quarantineDuration   time.Duration
	now                  func() time.Time
	logf                 func(msg string)
}

// SetClock replaces the clock used for heartbeat times; for tests.
func (r *WorkerRegistry) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// SetLogger sets the sink for worker liveness lines, which are plain text
// without a component or timestamp prefix. The default discards them.
func (r *WorkerRegistry) SetLogger(logf func(msg string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logf = logf
}

func (r *WorkerRegistry) logLocked(format string, args ...any) {
	if r.logf != nil {
		r.logf(fmt.Sprintf(format, args...))
	}
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
		now:                  time.Now,
	}
}

// IsHostInfrastructureFailure returns true if the exit code represents a host environment,
// sandbox, or hypervisor failure (codes 2, 3, 4, 9) rather than a package recipe error (code 1) or cancel (137).
func IsHostInfrastructureFailure(exitCode int) bool {
	return exitCode != 0 && exitCode != 1 && exitCode != 137
}

func (r *WorkerRegistry) UpdateHeartbeat(hb v1.WorkerHeartbeat) {
	legacy := reportsNoAvailability(&hb)
	hb = withIdleAvailability(hb)
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[hb.WorkerID]
	if !exists {
		r.workers[hb.WorkerID] = &WorkerRecord{
			WorkerHeartbeat: hb,
			LastSeen:        r.now(),
			noAvailability:  legacy,
		}
		return
	}

	now := r.now()
	if w.silenceLogged || w.statusLogged {
		r.logLocked("worker %s heartbeat resumed after %ds", hb.WorkerID, seconds(now.Sub(w.LastSeen)))
	}
	w.silenceLogged, w.statusLogged = false, false
	w.WorkerHeartbeat = hb
	w.LastSeen = now
	w.noAvailability = legacy
	acknowledgeAllocations(w)
}

// withIdleAvailability fills in the availability of a worker that lists no
// job and reports none free: it does not advertise availability (an older
// worker), and a worker without jobs has all its capacity free. A worker that
// lists jobs and reports 0 free is really full and stays so.
func withIdleAvailability(hb v1.WorkerHeartbeat) v1.WorkerHeartbeat {
	hw := &hb.Hardware
	if reportsNoAvailability(&hb) {
		hw.AvailableVCPUs = hw.TotalVCPUs
		hw.AvailableMemoryMB = hw.TotalMemoryMB
	}
	return hb
}

func reportsNoAvailability(hb *v1.WorkerHeartbeat) bool {
	hw := &hb.Hardware
	return hw.AvailableVCPUs == 0 && hw.AvailableMemoryMB == 0 && hb.ActiveJobID == "" && len(hb.ActiveJobIDs) == 0
}

// acknowledgeAllocations marks the allocations of the jobs the worker's
// heartbeat lists as active: its availability now accounts for them.
func acknowledgeAllocations(w *WorkerRecord) {
	for _, id := range append([]string{w.ActiveJobID}, w.ActiveJobIDs...) {
		if a, ok := w.Allocations[id]; ok && !a.acknowledged {
			a.acknowledged = true
			w.Allocations[id] = a
		}
	}
}

// advertisesCapacity reports whether the worker states its capacity. A worker
// that does not (no totals) is treated as a single-job worker.
func advertisesCapacity(hw *v1.WorkerHardware) bool {
	return hw.TotalVCPUs > 0 || hw.TotalMemoryMB > 0
}

// freeCapacity is the worker's free capacity as its heartbeat reports it,
// less the jobs dispatched but not yet listed by a heartbeat.
func freeCapacity(w *WorkerRecord, now time.Time) v1.WorkerHardware {
	hw := w.Hardware
	for _, a := range w.Allocations {
		if a.acknowledged || now.Sub(a.dispatched) > pendingAckTimeout {
			continue
		}
		hw.AvailableMemoryMB -= a.memoryMB
		hw.AvailableVCPUs -= a.vcpus
	}
	return hw
}

func hasRoom(hw *v1.WorkerHardware) bool {
	return hw.AvailableMemoryMB >= 1024 && hw.AvailableVCPUs >= 1
}

func seconds(d time.Duration) int { return int(d / time.Second) }

// WarnSilentWorkers logs once per silence each known worker whose last
// heartbeat is older than the TTL.
func (r *WorkerRegistry) WarnSilentWorkers(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.workers {
		silent := now.Sub(w.LastSeen)
		if silent <= r.ttl || w.silenceLogged {
			continue
		}
		w.silenceLogged = true
		r.logLocked("WARN worker %s silent for %ds (TTL %ds, last heartbeat %s, %d jobs dispatched)",
			id, seconds(silent), seconds(r.ttl), w.LastSeen.Format("15:04:05"), len(w.Allocations))
	}
}

// NoteJobStatus records a job status message from a worker. When the worker's
// heartbeats have stopped while it still reports jobs, it logs once per
// silence that heartbeat publishing is probably failing.
func (r *WorkerRegistry) NoteJobStatus(workerID string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[workerID]
	if !ok || w.statusLogged {
		return
	}
	silent := now.Sub(w.LastSeen)
	if silent <= r.ttl {
		return
	}
	w.statusLogged = true
	r.logLocked("WARN worker %s sends job status but no heartbeats for %ds (heartbeat publish failing?)",
		workerID, seconds(silent))
}

// HeartbeatAge is the time since the worker's last heartbeat; false for an
// unknown worker.
func (r *WorkerRegistry) HeartbeatAge(workerID string, now time.Time) (time.Duration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[workerID]
	if !ok {
		return 0, false
	}
	return now.Sub(w.LastSeen), true
}

// LastSeen is the time of the worker's last heartbeat, the zero time for an
// unknown worker.
func (r *WorkerRegistry) LastSeen(workerID string) time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if w, ok := r.workers[workerID]; ok {
		return w.LastSeen
	}
	return time.Time{}
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

// AllocateJobResources records the capacity of a dispatched job as pending on
// the worker. It stays pending until a heartbeat lists the job, so a
// heartbeat sent before the worker started the job does not hand its capacity
// out again.
func (r *WorkerRegistry) AllocateJobResources(workerID string, job *v1.BuildJob) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[workerID]
	if !exists {
		return
	}

	// A worker that does not advertise availability runs one job at a time.
	if (w.noAvailability || !advertisesCapacity(&w.Hardware)) && len(w.ActiveJobIDs) == 0 {
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
	if w.Allocations == nil {
		w.Allocations = make(map[string]jobAllocation)
	}
	w.Allocations[job.JobID] = jobAllocation{memoryMB: reqMem, vcpus: reqVCPUs, dispatched: r.now()}

	w.ActiveJobID = job.JobID
	w.ActiveJobIDs = append(w.ActiveJobIDs, job.JobID)
	w.Status = statusFor(w, r.now())
}

func statusFor(w *WorkerRecord, now time.Time) string {
	free := freeCapacity(w, now)
	if !advertisesCapacity(&w.Hardware) || hasRoom(&free) {
		return "IDLE"
	}
	return "BUSY"
}

// ReleaseJobResources forgets the allocation of a job that finished, failed,
// was refused or taken back. The worker's own availability returns with its
// next heartbeat.
func (r *WorkerRegistry) ReleaseJobResources(workerID, jobID string, _ *v1.ResourceRequirements) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[workerID]
	if !exists {
		return
	}
	delete(w.Allocations, jobID)

	var remaining []string
	for _, id := range w.ActiveJobIDs {
		if id != jobID {
			remaining = append(remaining, id)
		}
	}
	w.ActiveJobIDs = remaining
	w.ActiveJobID = ""
	if len(remaining) > 0 {
		w.ActiveJobID = remaining[len(remaining)-1]
	}
	if w.Status == "BUSY" || w.Status == "IDLE" {
		w.Status = statusFor(w, r.now())
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

	now := r.now()
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
	free := freeCapacity(w, now)
	if w.Status == "BUSY" && (w.noAvailability || !hasRoom(&free)) {
		return false
	}
	return matchesConstraints(&free, &job.Resources)
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
// the free resources anywhere: among the workers the job fits when idle, is
// big for (see isBigFor) and that are not excluded, the one running the fewest jobs, then the one with
// the most free vCPUs, then the lowest ID. It returns "" when none exists.
func (r *WorkerRegistry) FindReservableWorker(job *v1.BuildJob, excluded map[string]bool) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var best *WorkerRecord
	for _, w := range r.workers {
		if excluded[w.WorkerID] || !isBigFor(w, job) || !r.fitsWhenIdle(w, job, now) {
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

// isBigFor reports whether the job asks for more than half of the worker's
// total vCPUs or memory. Only such jobs may reserve a worker: small jobs
// backfill instead of serializing dispatch behind reservations.
func isBigFor(w *WorkerRecord, job *v1.BuildJob) bool {
	hw := &w.Hardware
	return int64(job.Resources.VCPUs)*2 > int64(hw.TotalVCPUs) ||
		job.Resources.MemoryMB*2 > hw.TotalMemoryMB
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

// matchesConstraints reports whether the free capacity hw meets req. A fully
// allocated worker (available 0) has no room; only a worker that states no
// capacity at all is not checked by vCPUs and memory.
func matchesConstraints(hw *v1.WorkerHardware, req *v1.ResourceRequirements) bool {
	if advertisesCapacity(hw) {
		if req.MemoryMB > 0 && hw.AvailableMemoryMB < int64(req.MemoryMB) {
			return false
		}
		if req.VCPUs > 0 && hw.AvailableVCPUs < int32(req.VCPUs) {
			return false
		}
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
