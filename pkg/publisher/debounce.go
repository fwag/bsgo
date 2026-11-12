package publisher

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// DebounceConfig sets timing windows for debouncing PublishRequests.
type DebounceConfig struct {
	Window  time.Duration // Quiet window before triggering publish (e.g. 2s)
	MaxWait time.Duration // Maximum wait before forced publish (e.g. 10s)
}

// DebounceManager coalesces multiple incoming PublishRequests for the same repository.
type DebounceManager struct {
	mu        sync.Mutex
	publisher *Publisher
	cfg       DebounceConfig
	batches   map[string]*repoBatch
	closed    bool
}

type batchWaiter struct {
	resChan chan *PublishResult
	errChan chan error
}

type repoBatch struct {
	key       string
	createdAt time.Time
	req       v1.PublishRequest
	timer     *time.Timer
	waiters   []batchWaiter
}

// NewDebounceManager creates a new repository debounce batcher.
func NewDebounceManager(pub *Publisher, cfg DebounceConfig) *DebounceManager {
	if cfg.Window <= 0 {
		cfg.Window = 2 * time.Second
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 10 * time.Second
	}
	return &DebounceManager{
		publisher: pub,
		cfg:       cfg,
		batches:   make(map[string]*repoBatch),
	}
}

// Enqueue submits a publish request to be coalesced with pending requests for the same repository.
func (d *DebounceManager) Enqueue(ctx context.Context, req v1.PublishRequest) (*PublishResult, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, fmt.Errorf("debounce manager is closed")
	}

	key := fmt.Sprintf("%s:%s:%s", req.Project, req.Target, req.Architecture)
	batch, exists := d.batches[key]

	waiter := batchWaiter{
		resChan: make(chan *PublishResult, 1),
		errChan: make(chan error, 1),
	}

	if !exists {
		batch = d.newBatch(key, req)
		d.batches[key] = batch
	} else {
		mergePublishRequest(&batch.req, req)
		d.adjustTimer(batch)
	}

	batch.waiters = append(batch.waiters, waiter)
	d.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-waiter.errChan:
		return nil, err
	case res := <-waiter.resChan:
		return res, nil
	}
}

func (d *DebounceManager) newBatch(key string, req v1.PublishRequest) *repoBatch {
	b := &repoBatch{
		key:       key,
		createdAt: time.Now(),
		req:       req,
		waiters:   make([]batchWaiter, 0),
	}
	b.timer = time.AfterFunc(d.cfg.Window, func() {
		d.flushBatch(key)
	})
	return b
}

func (d *DebounceManager) adjustTimer(b *repoBatch) {
	if time.Since(b.createdAt) >= d.cfg.MaxWait {
		return
	}
	b.timer.Stop()
	b.timer.Reset(d.cfg.Window)
}

func (d *DebounceManager) flushBatch(key string) {
	d.mu.Lock()
	batch, exists := d.batches[key]
	if !exists {
		d.mu.Unlock()
		return
	}
	delete(d.batches, key)
	d.mu.Unlock()

	res, err := d.publisher.ProcessPublishRequest(context.Background(), batch.req)
	for _, w := range batch.waiters {
		if err != nil {
			w.errChan <- err
		} else {
			w.resChan <- res
		}
	}
}

// Flush immediately flushes all pending batches synchronously.
func (d *DebounceManager) Flush() {
	d.mu.Lock()
	var keys []string
	for k, b := range d.batches {
		b.timer.Stop()
		keys = append(keys, k)
	}
	d.mu.Unlock()

	for _, k := range keys {
		d.flushBatch(k)
	}
}

// Close stops all timers and clears batches.
func (d *DebounceManager) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, b := range d.batches {
		b.timer.Stop()
	}
	d.batches = make(map[string]*repoBatch)
}

func mergePublishRequest(dst *v1.PublishRequest, src v1.PublishRequest) {
	dst.NewRPMPaths = mergeUniqueStrings(dst.NewRPMPaths, src.NewRPMPaths)
	dst.ArtifactKeys = mergeUniqueStrings(dst.ArtifactKeys, src.ArtifactKeys)
	dst.DeletedRPMs = mergeUniqueStrings(dst.DeletedRPMs, src.DeletedRPMs)
	dst.SubRepos = mergeUniqueStrings(dst.SubRepos, src.SubRepos)
	dst.RepoTypes = mergeUniqueStrings(dst.RepoTypes, src.RepoTypes)

	if len(src.Advisories) > 0 {
		advMap := make(map[string]bool)
		for _, a := range dst.Advisories {
			advMap[a.ID] = true
		}
		for _, a := range src.Advisories {
			if !advMap[a.ID] {
				dst.Advisories = append(dst.Advisories, a)
				advMap[a.ID] = true
			}
		}
	}

	if len(src.AppStreamData) > 0 {
		dst.AppStreamData = src.AppStreamData
	}
	if src.AppStreamPath != "" {
		dst.AppStreamPath = src.AppStreamPath
	}
}

func mergeUniqueStrings(a, b []string) []string {
	seen := make(map[string]bool)
	var res []string
	for _, s := range a {
		norm := strings.TrimSpace(s)
		if norm != "" && !seen[norm] {
			seen[norm] = true
			res = append(res, s)
		}
	}
	for _, s := range b {
		norm := strings.TrimSpace(s)
		if norm != "" && !seen[norm] {
			seen[norm] = true
			res = append(res, s)
		}
	}
	return res
}
