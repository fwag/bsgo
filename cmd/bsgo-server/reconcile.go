package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const (
	reconcileInterval = time.Minute
	// orphanGrace is how long a dispatching or building row may go without its job being
	// reported by any worker before it is requeued. It covers the gap
	// between dispatch and the worker's first heartbeat with the job.
	orphanGrace = 3 * time.Minute
)

// startOrphanReconciler requeues builds that no worker runs any more: the
// worker or the whole host was restarted, or the build container was killed.
// Their rows would otherwise stay "building" forever, because the dispatcher
// state is in memory and the checker keeps in-flight rows as they are.
func startOrphanReconciler(ctx context.Context, store storage.Store, disp *dispatcher.Dispatcher, nc *nats.Conn) {
	go func() {
		t := time.NewTicker(reconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := requeueOrphans(ctx, store, disp, nc, time.Now()); err != nil {
					fmt.Fprintf(os.Stderr, "[%s] [RECONCILE] %v\n", time.Now().Format("15:04:05"), err)
				}
			}
		}
	}()
}

func requeueOrphans(ctx context.Context, store storage.Store, disp *dispatcher.Dispatcher, nc *nats.Conn, now time.Time) error {
	for _, st := range orphanedBuilds(ctx, store, disp, now) {
		msg, err := json.Marshal(map[string]any{
			"job_id":       st.JobID,
			"project":      st.Project,
			"package":      buildrepo.UnitName(st.Package, st.Flavor),
			"target":       st.Repo,
			"architecture": st.Arch,
			"phase":        "REQUEUED",
			"reason":       "NO_WORKER_RUNS_JOB",
		})
		if err != nil {
			return err
		}
		if err := nc.Publish("bsgo.status", msg); err != nil {
			return fmt.Errorf("publish requeue of %s: %w", st.JobID, err)
		}
		fmt.Printf("[%s] [RECONCILE] Requeued orphaned build %s/%s %s/%s (job %s)\n",
			now.Format("15:04:05"), st.Project, buildrepo.UnitName(st.Package, st.Flavor), st.Repo, st.Arch, st.JobID)
	}
	return nil
}

// orphanedBuilds lists dispatching and building rows older than orphanGrace whose job is
// neither reported by a worker nor tracked as dispatched.
func orphanedBuilds(ctx context.Context, store storage.Store, disp *dispatcher.Dispatcher, now time.Time) []*storage.PackageBuildState {
	prjs, err := store.ListProjects(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [RECONCILE] list projects: %v\n", now.Format("15:04:05"), err)
		return nil
	}
	var out []*storage.PackageBuildState
	for _, p := range prjs {
		for _, state := range []string{storage.StateDispatching, storage.StateBuilding} {
			rows, err := store.ListBuildStates(ctx, p.Project.Name, storage.BuildStateFilter{State: state})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] [RECONCILE] list %s rows of %s: %v\n", now.Format("15:04:05"), state, p.Project.Name, err)
				continue
			}
			for _, st := range rows {
				if isOrphan(st, disp, now) {
					out = append(out, st)
				}
			}
		}
	}
	return out
}

// isOrphan reports whether a dispatching or building row is older than
// orphanGrace and its job is neither reported by a worker nor tracked as
// dispatched.
func isOrphan(st *storage.PackageBuildState, disp *dispatcher.Dispatcher, now time.Time) bool {
	if st.JobID == "" || now.Sub(st.UpdatedAt) < orphanGrace {
		return false
	}
	return !disp.IsRunning(st.JobID) && !disp.IsDispatched(st.JobID)
}
