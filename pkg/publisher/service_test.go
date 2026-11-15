package publisher_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
)

func newReplica(h *harness, shared coord.Coordinator, owner string) *publisher.Service {
	return publisher.NewService(h.pub, publisher.ServiceConfig{Coord: shared, Owner: owner})
}

func TestServicePublishesMarkedPrpa(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	shared := &coord.Memory{}
	svc := newReplica(h, shared, "r1")
	if n, err := svc.Step(context.Background()); n != 0 || err != nil {
		t.Fatalf("nothing marked yet, got %d, %v", n, err)
	}
	if err := svc.MarkChanged(context.Background(), testPrpa); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.Step(context.Background()); n != 1 || err != nil {
		t.Fatalf("step handled %d, %v", n, err)
	}
	if !h.store.Has(stdPrefix + "repodata/repomd.xml") {
		t.Error("marked prpa was not published")
	}
	if n, _ := svc.Step(context.Background()); n != 0 {
		t.Error("a published prpa must not be published again until it changes")
	}
}

// TestTwoReplicasNeverPublishOnePrpaConcurrently has replica r2 try to
// publish while r1 is in the middle of its publish: r2 must back off.
func TestTwoReplicasNeverPublishOnePrpaConcurrently(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	var active, maxActive atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.cr.gate = func() {
		cur := active.Add(1)
		for {
			m := maxActive.Load()
			if cur <= m || maxActive.CompareAndSwap(m, cur) {
				break
			}
		}
		once.Do(func() { close(entered) })
		<-release
		active.Add(-1)
	}
	shared := &coord.Memory{TTL: time.Minute}
	r1, r2 := newReplica(h, shared, "r1"), newReplica(h, shared, "r2")
	ctx := context.Background()
	if err := r1.MarkChanged(ctx, testPrpa); err != nil {
		t.Fatal(err)
	}

	done := make(chan int)
	go func() {
		n, _ := r1.Step(ctx)
		done <- n
	}()
	<-entered
	if n, err := r2.Step(ctx); n != 0 || err != nil {
		t.Errorf("r2 handled %d prpas (%v) while r1 holds the lease", n, err)
	}
	close(release)
	if n := <-done; n != 1 {
		t.Errorf("r1 handled %d prpas, want 1", n)
	}
	if m := maxActive.Load(); m != 1 {
		t.Errorf("%d publishes of one prpa ran at the same time", m)
	}
	if n, _ := r2.Step(ctx); n != 0 {
		t.Error("the prpa was already published; r2 has nothing to do")
	}
	if len(h.cr.calls) != 1 {
		t.Errorf("createrepo ran %d times, want once", len(h.cr.calls))
	}
}

func TestFailedPublishStaysPending(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	h.cr.err = context.DeadlineExceeded
	var failed []string
	svc := publisher.NewService(h.pub, publisher.ServiceConfig{
		Coord: &coord.Memory{}, Owner: "r1",
		OnError: func(prpa string, _ error) { failed = append(failed, prpa) },
	})
	ctx := context.Background()
	if err := svc.MarkChanged(ctx, testPrpa); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 {
		t.Fatalf("errors %v", failed)
	}
	h.cr.err = nil
	if n, _ := svc.Step(ctx); n != 1 || !h.store.Has(stdPrefix+"repodata/repomd.xml") {
		t.Errorf("the failed prpa must be retried and published (handled %d)", n)
	}
}
