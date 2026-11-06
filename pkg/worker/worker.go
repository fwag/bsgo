package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type WorkerDaemon struct {
	mu           sync.Mutex
	cfg          WorkerConfig
	nc           *nats.Conn
	sandbox      Sandbox
	status       string
	activeJobID  string
	stopChan     chan struct{}
	cachedHashes []string
}

func NewWorkerDaemon(cfg WorkerConfig, nc *nats.Conn, sandbox Sandbox) *WorkerDaemon {
	return &WorkerDaemon{
		cfg:      cfg,
		nc:       nc,
		sandbox:  sandbox,
		status:   "IDLE",
		stopChan: make(chan struct{}),
	}
}

func (w *WorkerDaemon) Start(ctx context.Context) error {
	go w.runHeartbeats(ctx)

	subject := fmt.Sprintf("bsgo.workers.%s.job", w.cfg.WorkerID)
	sub, err := w.nc.Subscribe(subject, func(msg *nats.Msg) {
		var job v1.BuildJob
		if err := json.Unmarshal(msg.Data, &job); err != nil {
			return
		}
		go w.processJob(ctx, &job)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to worker job inbox: %w", err)
	}

	select {
	case <-ctx.Done():
	case <-w.stopChan:
	}
	return sub.Unsubscribe()
}

func (w *WorkerDaemon) Stop() {
	close(w.stopChan)
}

func (w *WorkerDaemon) Status() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *WorkerDaemon) runHeartbeats(ctx context.Context) {
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
	hb := v1.WorkerHeartbeat{
		WorkerID:        w.cfg.WorkerID,
		Architecture:    w.cfg.Architecture,
		Status:          w.status,
		ActiveJobID:     w.activeJobID,
		Hardware:        w.cfg.Hardware,
		CachedRPMHashes: w.cachedHashes,
		TimestampUnix:   time.Now().Unix(),
	}
	w.mu.Unlock()

	data, err := json.Marshal(hb)
	if err == nil {
		_ = w.nc.Publish("bsgo.workers.heartbeat", data)
	}
}

func (w *WorkerDaemon) processJob(parentCtx context.Context, job *v1.BuildJob) {
	w.mu.Lock()
	w.status = "BUSY"
	w.activeJobID = job.JobID
	w.mu.Unlock()
	w.emitHeartbeat()

	jobCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Control / abort listener
	ctrlSub, _ := w.nc.Subscribe(fmt.Sprintf("bsgo.jobs.%s.control", job.JobID), func(msg *nats.Msg) {
		var ctrl v1.JobControl
		if err := json.Unmarshal(msg.Data, &ctrl); err == nil && ctrl.Action == "ABORT" {
			cancel()
		}
	})
	if ctrlSub != nil {
		_ = w.nc.Flush()
		defer func() { _ = ctrlSub.Unsubscribe() }()
	}

	logWriter := &natsLogWriter{nc: w.nc, jobID: job.JobID}
	res, execErr := w.sandbox.Execute(jobCtx, job, logWriter)

	phase := "SUCCEEDED"
	exitCode := 0
	if jobCtx.Err() != nil {
		phase = "ABORTED"
		exitCode = 137
	} else if execErr != nil || (res != nil && res.ExitCode != 0) {
		phase = "FAILED"
		if res != nil {
			exitCode = res.ExitCode
		} else {
			exitCode = 1
		}
	}

	w.publishStatusUpdate(job, phase, exitCode, res)

	w.mu.Lock()
	w.status = "IDLE"
	w.activeJobID = ""
	if phase == "SUCCEEDED" && res != nil {
		for _, a := range res.Artifacts {
			w.cachedHashes = append(w.cachedHashes, a.SHA256)
		}
	}
	w.mu.Unlock()
	w.emitHeartbeat()
}

func (w *WorkerDaemon) publishStatusUpdate(job *v1.BuildJob, phase string, exitCode int, res *ExecutionResult) {
	status := map[string]any{
		"job_id":       job.JobID,
		"project":      job.Project,
		"package":      job.Package,
		"architecture": job.Architecture,
		"worker_id":    w.cfg.WorkerID,
		"phase":        phase,
		"exit_code":    exitCode,
	}
	if res != nil {
		status["artifacts"] = res.Artifacts
		status["total_seconds"] = res.TotalSeconds
	}
	data, _ := json.Marshal(status)
	_ = w.nc.Publish("bsgo.status", data)
}

type natsLogWriter struct {
	nc    *nats.Conn
	jobID string
}

func (w *natsLogWriter) Write(p []byte) (int, error) {
	subject := fmt.Sprintf("bsgo.logs.%s", w.jobID)
	err := w.nc.Publish(subject, p)
	return len(p), err
}
