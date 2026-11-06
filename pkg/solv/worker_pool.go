package solv

import (
	"context"
	"fmt"
	"runtime"
)

type WorkerPool struct {
	arch    string
	jobChan chan workerJob
	stop    chan struct{}
}

type workerJob struct {
	req      ExpansionRequest
	respChan chan workerResponse
}

type workerResponse struct {
	result *ExpansionResult
	err    error
}

func NewWorkerPool(arch string, numWorkers int) (*WorkerPool, error) {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	wp := &WorkerPool{
		arch:    arch,
		jobChan: make(chan workerJob),
		stop:    make(chan struct{}),
	}

	for i := 0; i < numWorkers; i++ {
		go wp.runWorker()
	}

	return wp, nil
}

func (wp *WorkerPool) Close() {
	close(wp.stop)
}

func (wp *WorkerPool) runWorker() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	pool, err := NewPool(wp.arch)
	if err != nil {
		return
	}
	defer pool.Close()

	for {
		select {
		case <-wp.stop:
			return
		case job := <-wp.jobChan:
			res, resErr := pool.Resolve(job.req)
			job.respChan <- workerResponse{result: res, err: resErr}
		}
	}
}

func (wp *WorkerPool) Resolve(ctx context.Context, req ExpansionRequest) (*ExpansionResult, error) {
	respChan := make(chan workerResponse, 1)
	job := workerJob{
		req:      req,
		respChan: respChan,
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-wp.stop:
		return nil, fmt.Errorf("worker pool is stopped")
	case wp.jobChan <- job:
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-respChan:
		return resp.result, resp.err
	}
}
