package publisher_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
)

func TestDebounceBatchingCoalescence(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	mockCR := &MockCreateRepoExecutor{}
	mockS3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(mockCR, mockS3, lockMgr, tmpDir)

	cfg := publisher.DebounceConfig{
		Window:  50 * time.Millisecond,
		MaxWait: 200 * time.Millisecond,
	}
	debouncer := publisher.NewDebounceManager(pub, cfg)
	defer debouncer.Close()

	// Enqueue 5 requests for the same repository concurrently
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	results := make(chan *publisher.PublishResult, 5)

	for i := 1; i <= 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := v1.PublishRequest{
				RequestID:    "req-" + string(rune('0'+idx)),
				Project:      "openSUSE:Factory",
				Target:       "standard",
				Architecture: "x86_64",
				NewRPMPaths:  []string{filepath.Join(tmpDir, "pkg-"+string(rune('0'+idx))+".rpm")},
			}
			res, err := debouncer.Enqueue(ctx, req)
			if err != nil {
				errs <- err
			} else {
				results <- res
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	close(results)

	for err := range errs {
		t.Fatalf("unexpected error during enqueued publish: %v", err)
	}

	resCount := 0
	for range results {
		resCount++
	}
	if resCount != 5 {
		t.Errorf("expected 5 results returned to waiting callers, got %d", resCount)
	}

	// Because of debouncing within 50ms, createrepo_c should be called significantly fewer times than 5 (ideally 1)
	if len(mockCR.Dirs) > 2 {
		t.Errorf("expected debouncer to coalesce requests (<= 2 executions), got %d invocations", len(mockCR.Dirs))
	}
}

func TestDebounceFlush(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	mockCR := &MockCreateRepoExecutor{}
	mockS3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(mockCR, mockS3, lockMgr, tmpDir)

	cfg := publisher.DebounceConfig{
		Window:  10 * time.Second, // Long window
		MaxWait: 30 * time.Second,
	}
	debouncer := publisher.NewDebounceManager(pub, cfg)
	defer debouncer.Close()

	done := make(chan struct{})
	go func() {
		req := v1.PublishRequest{
			RequestID:    "req-flush-01",
			Project:      "openSUSE:Factory",
			Target:       "standard",
			Architecture: "x86_64",
		}
		_, _ = debouncer.Enqueue(ctx, req)
		close(done)
	}()

	// Give a few ms for Enqueue to register
	time.Sleep(20 * time.Millisecond)

	// Explicitly flush
	debouncer.Flush()

	select {
	case <-done:
		// success
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for debouncer Flush to complete")
	}

	if len(mockCR.Dirs) == 0 {
		t.Errorf("expected createrepo_c to be invoked after Flush")
	}
}
