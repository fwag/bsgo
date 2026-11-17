package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type activeJobContext struct {
	job               *v1.BuildJob
	cancel            context.CancelFunc
	allocatedMemoryMB int64
	allocatedVCPUs    int32
}

type WorkerDaemon struct {
	mu           sync.Mutex
	cfg          WorkerConfig
	nc           *nats.Conn
	sandbox      Sandbox
	status       string
	activeJobID  string
	activeJobs   map[string]*activeJobContext
	stopChan     chan struct{}
	stopOnce     sync.Once
	cachedHashes []string
	wg           sync.WaitGroup
}

func NewWorkerDaemon(cfg WorkerConfig, nc *nats.Conn, sandbox Sandbox) *WorkerDaemon {
	if cfg.Hardware.TotalMemoryMB <= 0 || cfg.Hardware.TotalVCPUs <= 0 {
		det, err := DetectHardware("/proc", "/sys/fs/cgroup", cfg.WorkDir)
		if err != nil {
			det = v1.WorkerHardware{TotalVCPUs: 1, TotalMemoryMB: 2048}
		}
		eff := EffectiveHardware(det, cfg.HardwareOverrides)
		if cfg.Hardware.TotalMemoryMB <= 0 {
			cfg.Hardware.TotalMemoryMB = eff.TotalMemoryMB
		}
		if cfg.Hardware.TotalVCPUs <= 0 {
			cfg.Hardware.TotalVCPUs = eff.TotalVCPUs
		}
	}
	if cfg.Hardware.AvailableMemoryMB <= 0 {
		cfg.Hardware.AvailableMemoryMB = cfg.Hardware.TotalMemoryMB
	}
	if cfg.Hardware.AvailableVCPUs <= 0 {
		cfg.Hardware.AvailableVCPUs = cfg.Hardware.TotalVCPUs
	}

	return &WorkerDaemon{
		cfg:          cfg,
		nc:           nc,
		sandbox:      sandbox,
		status:       "IDLE",
		activeJobs:   make(map[string]*activeJobContext),
		stopChan:     make(chan struct{}),
		cachedHashes: make([]string, 0),
	}
}

func (w *WorkerDaemon) Start(ctx context.Context) error {
	if oc, ok := w.sandbox.(interface{ CleanupOrphans(context.Context) error }); ok {
		_ = oc.CleanupOrphans(ctx)
	}

	go w.runHeartbeats(ctx)

	subject := fmt.Sprintf("bsgo.workers.%s.job", w.cfg.WorkerID)
	sub, err := w.nc.Subscribe(subject, func(msg *nats.Msg) {
		var job v1.BuildJob
		if err := json.Unmarshal(msg.Data, &job); err != nil {
			return
		}
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.processJob(ctx, &job)
		}()
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to worker job inbox: %w", err)
	}

	fmt.Printf("[%s] [INFO] Worker registered: id=%s arch=%s builds=%s (%d vCPUs, %dGB RAM)\n",
		time.Now().Format("15:04:05"), w.cfg.WorkerID, w.cfg.Architecture, strings.Join(w.cfg.BuildArchs(), ","), w.cfg.Hardware.TotalVCPUs, w.cfg.Hardware.TotalMemoryMB/1024)
	fmt.Printf("[%s] [INFO] Listening for build jobs on: %s\n", time.Now().Format("15:04:05"), subject)
	fmt.Printf("[%s] [INFO] Emitting heartbeats to: bsgo.workers.heartbeat (Status: IDLE)\n", time.Now().Format("15:04:05"))
	fmt.Printf("[%s] [INFO] Ready for jobs. Waiting for server dispatch...\n", time.Now().Format("15:04:05"))

	select {
	case <-ctx.Done():
	case <-w.stopChan:
	}
	_ = sub.Unsubscribe()
	w.wg.Wait()
	return nil
}

func (w *WorkerDaemon) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopChan)
		w.mu.Lock()
		for _, j := range w.activeJobs {
			if j.cancel != nil {
				j.cancel()
			}
		}
		w.mu.Unlock()
	})
}

func (w *WorkerDaemon) Status() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *WorkerDaemon) ActiveJobs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var res []string
	for id := range w.activeJobs {
		res = append(res, id)
	}
	return res
}

func (w *WorkerDaemon) runHeartbeats(ctx context.Context) {
	w.emitHeartbeat()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopChan:
			return
		case <-ticker.C:
			w.emitHeartbeat()
		}
	}
}

func (w *WorkerDaemon) emitHeartbeat() {
	w.mu.Lock()
	var activeIDs []string
	for id := range w.activeJobs {
		activeIDs = append(activeIDs, id)
	}
	var activeID string
	if len(activeIDs) > 0 {
		activeID = activeIDs[0]
	}

	hb := v1.WorkerHeartbeat{
		WorkerID:        w.cfg.WorkerID,
		Architecture:    w.cfg.Architecture,
		Archs:           w.cfg.BuildArchs(),
		Status:          w.status,
		ActiveJobID:     activeID,
		ActiveJobIDs:    activeIDs,
		Hardware:        w.cfg.Hardware,
		CachedRPMHashes: w.cachedHashes,
		ConstrainedOnly: w.cfg.ConstrainedOnly,
		TimestampUnix:   time.Now().Unix(),
	}
	w.mu.Unlock()

	data, err := json.Marshal(hb)
	if err == nil {
		_ = w.nc.Publish("bsgo.workers.heartbeat", data)
		if w.cfg.StateDir != "" {
			_ = os.MkdirAll(w.cfg.StateDir, 0750)
			_ = os.WriteFile(filepath.Join(w.cfg.StateDir, "status.json"), data, 0600)
		}
	}
}

func (w *WorkerDaemon) processJob(parentCtx context.Context, job *v1.BuildJob) {
	jobCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	reqMem, reqVCPUs, ok := w.allocateJob(job, cancel)
	if !ok {
		fmt.Printf("[%s] [JOB] %s: already running, ignoring the repeated dispatch\n",
			time.Now().Format("15:04:05"), job.JobID)
		return
	}
	defer w.releaseJob(job.JobID, reqMem, reqVCPUs)

	if ctrlSub := w.subscribeJobAbort(job.JobID, cancel); ctrlSub != nil {
		defer func() { _ = ctrlSub.Unsubscribe() }()
	}

	w.publishStatusUpdate(job, "BUILDING", 0, nil, nil)

	natsLog := &natsLogWriter{nc: w.nc, jobID: job.JobID}
	res, execErr := w.sandbox.Execute(jobCtx, job, natsLog)

	phase, exitCode := determineJobOutcome(jobCtx, execErr, res)
	w.publishStatusUpdate(job, phase, exitCode, res, execErr)

	w.mu.Lock()
	if phase == "SUCCEEDED" && res != nil {
		for _, a := range res.Artifacts {
			w.cachedHashes = append(w.cachedHashes, a.SHA256)
		}
	}
	w.mu.Unlock()

	logJobCompletion(job, phase, exitCode, res)
}

// allocateJob reserves the resources of job and registers it as active. It
// reports false, changing nothing, when the worker already runs a job with
// this ID: a repeated dispatch must neither start a second build in the same
// job directory nor disturb the running one.
func (w *WorkerDaemon) allocateJob(job *v1.BuildJob, cancel context.CancelFunc) (int64, int32, bool) {
	w.mu.Lock()
	if _, running := w.activeJobs[job.JobID]; running {
		w.mu.Unlock()
		return 0, 0, false
	}
	reqVCPUs, reqMem := grantResources(job.Resources, w.cfg.Hardware)
	job.Resources.VCPUs = reqVCPUs
	job.Resources.MemoryMB = reqMem
	w.cfg.Hardware.AvailableMemoryMB -= reqMem
	if w.cfg.Hardware.AvailableMemoryMB < 0 {
		w.cfg.Hardware.AvailableMemoryMB = 0
	}
	w.cfg.Hardware.AvailableVCPUs -= reqVCPUs
	if w.cfg.Hardware.AvailableVCPUs < 0 {
		w.cfg.Hardware.AvailableVCPUs = 0
	}
	w.activeJobID = job.JobID
	w.activeJobs[job.JobID] = &activeJobContext{
		job:               job,
		cancel:            cancel,
		allocatedMemoryMB: reqMem,
		allocatedVCPUs:    reqVCPUs,
	}
	if w.cfg.Hardware.AvailableMemoryMB < 1024 || w.cfg.Hardware.AvailableVCPUs < 1 {
		w.status = "BUSY"
	} else {
		w.status = "IDLE"
	}
	w.mu.Unlock()
	w.emitHeartbeat()

	fmt.Printf("[%s] [JOB] %s: started build for %s/%s (arch: %s)\n",
		time.Now().Format("15:04:05"), job.JobID, job.Project, job.Package, job.Architecture)
	return reqMem, reqVCPUs, true
}

func (w *WorkerDaemon) releaseJob(jobID string, reqMem int64, reqVCPUs int32) {
	w.mu.Lock()
	w.cfg.Hardware.AvailableMemoryMB += reqMem
	if w.cfg.Hardware.AvailableMemoryMB > w.cfg.Hardware.TotalMemoryMB {
		w.cfg.Hardware.AvailableMemoryMB = w.cfg.Hardware.TotalMemoryMB
	}
	w.cfg.Hardware.AvailableVCPUs += reqVCPUs
	if w.cfg.Hardware.AvailableVCPUs > w.cfg.Hardware.TotalVCPUs {
		w.cfg.Hardware.AvailableVCPUs = w.cfg.Hardware.TotalVCPUs
	}
	delete(w.activeJobs, jobID)
	if len(w.activeJobs) == 0 {
		w.activeJobID = ""
		w.status = "IDLE"
	} else {
		for id := range w.activeJobs {
			w.activeJobID = id
			break
		}
		if w.cfg.Hardware.AvailableMemoryMB >= 1024 && w.cfg.Hardware.AvailableVCPUs >= 1 {
			w.status = "IDLE"
		} else {
			w.status = "BUSY"
		}
	}
	w.mu.Unlock()
	w.emitHeartbeat()
}

func (w *WorkerDaemon) subscribeJobAbort(jobID string, cancel context.CancelFunc) *nats.Subscription {
	sub, _ := w.nc.Subscribe(fmt.Sprintf("bsgo.jobs.%s.control", jobID), func(msg *nats.Msg) {
		var ctrl v1.JobControl
		if err := json.Unmarshal(msg.Data, &ctrl); err == nil && ctrl.Action == "ABORT" {
			if ctrl.JobID == jobID {
				cancel()
			}
		}
	})
	if sub != nil {
		_ = w.nc.Flush()
	}
	return sub
}

func logJobCompletion(job *v1.BuildJob, phase string, exitCode int, res *ExecutionResult) {
	if phase == "SUCCEEDED" {
		artCount := 0
		if res != nil {
			artCount = len(res.Artifacts)
		}
		totalSec := 0.0
		if res != nil {
			totalSec = res.TotalSeconds
		}
		fmt.Printf("[%s] [JOB] %s: build SUCCEEDED in %.2fs (%d artifacts)\n",
			time.Now().Format("15:04:05"), job.JobID, totalSec, artCount)
	} else {
		fmt.Printf("[%s] [JOB] %s: build %s (exit code: %d)\n",
			time.Now().Format("15:04:05"), job.JobID, phase, exitCode)
	}
}

func determineJobOutcome(jobCtx context.Context, execErr error, res *ExecutionResult) (string, int) {
	if jobCtx.Err() != nil {
		return "ABORTED", 137
	}
	if res != nil && res.Aborted {
		return "ABORTED", res.ExitCode
	}
	if execErr != nil || (res != nil && res.ExitCode != 0) {
		if res != nil {
			return "FAILED", res.ExitCode
		}
		return "FAILED", 1
	}
	return "SUCCEEDED", 0
}

func (w *WorkerDaemon) publishStatusUpdate(job *v1.BuildJob, phase string, exitCode int, res *ExecutionResult, execErr error) {
	status := map[string]any{
		"job_id":       job.JobID,
		"project":      job.Project,
		"package":      job.Package,
		"target":       job.Target,
		"architecture": job.Architecture,
		"worker_id":    w.cfg.WorkerID,
		"phase":        phase,
		"exit_code":    exitCode,
	}
	if execErr != nil {
		status["reason"] = execErr.Error()
	} else if phase == "ABORTED" {
		status["reason"] = "aborted by control message or worker shutdown"
	}
	if res != nil {
		status["artifacts"] = res.Artifacts
		status["total_seconds"] = res.TotalSeconds
		addBuildProvenance(status, res)
	}
	data, _ := json.Marshal(status)
	_ = w.nc.Publish("bsgo.status", data)
}

// addBuildProvenance adds the fields downstream consumers (indexer, build
// state store) need: the exact source commit, the meta and the release.
func addBuildProvenance(status map[string]any, res *ExecutionResult) {
	if res.SourceCommit != "" {
		status["source_commit"] = res.SourceCommit
	}
	if res.Meta != "" {
		status["meta"] = res.Meta
	}
	if res.Release != "" {
		status["release"] = res.Release
	}
}

type natsLogWriter struct {
	nc    *nats.Conn
	jobID string
}

func (w *natsLogWriter) Write(p []byte) (n int, err error) {
	subject := fmt.Sprintf("bsgo.logs.%s", w.jobID)
	err = w.nc.Publish(subject, p)
	return len(p), err
}

// grantResources returns the vCPUs and memory a job is granted: its request
// (defaults 1 vCPU / 2048 MiB) capped at the worker's effective capacity. The
// sandbox, including obs-build --jobs, sees only the granted values.
func grantResources(req v1.ResourceRequirements, total v1.WorkerHardware) (int32, int64) {
	vcpus := req.VCPUs
	if vcpus <= 0 {
		vcpus = 1
	}
	mem := req.MemoryMB
	if mem <= 0 {
		mem = 2048
	}
	return min(vcpus, max(total.TotalVCPUs, 1)), min(mem, total.TotalMemoryMB)
}
