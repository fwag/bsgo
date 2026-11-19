package checker_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// priorities runs a pass and returns the priority of every emitted job.
func (w *world) priorities() map[string]int32 {
	w.t.Helper()
	w.run()
	out := map[string]int32{}
	for _, j := range w.sink.take() {
		out[j.Package] = j.Priority
	}
	return out
}

// TestJobPriority: a and b form a cycle; d and e depend on one
// member each, f is independent. A cycle member counts the dependents of the
// whole cycle (a, b, d, e), a plain unit its own dependents.
func TestJobPriority(t *testing.T) {
	var boot []bininfo.BinInfo
	for _, n := range []string{"a", "b", "d", "e", "f", "ext"} {
		boot = append(boot, binary(n, "0"))
	}
	w := newWorld(t, boot)
	w.pkg("a", []string{"b", "ext"})
	w.pkg("b", []string{"a"})
	w.pkg("d", []string{"a"})
	w.pkg("e", []string{"b"})
	w.pkg("f", nil)
	got := w.priorities()
	want := map[string]int32{"a": 54, "b": 54, "f": 50}
	for name, p := range want {
		if got[name] != p {
			t.Errorf("priority of %s = %d, want %d", name, got[name], p)
		}
	}
}

func TestForcedTriggerPriority(t *testing.T) {
	tests := []struct {
		tier string
		want int32
	}{
		{"", 50},
		{"low", 50},
		{"med", 50 + 5000},
		{"high", 50 + 20000},
	}
	for _, tt := range tests {
		t.Run(tt.tier, func(t *testing.T) {
			w := newWorld(t, []bininfo.BinInfo{binary("p", "0")})
			w.pkg("p", nil)
			w.run()
			first := w.wantJobs("p")[0]
			w.finish(first, binary("p", "1"))
			w.run()
			w.wantJobs()
			err := w.store.RequestForcedRebuild(context.Background(),
				storage.ForcedRebuild{Project: testProject, Package: "p", Priority: tt.tier})
			if err != nil {
				t.Fatal(err)
			}
			if got := w.priorities()["p"]; got != tt.want {
				t.Errorf("forced job priority %d, want %d", got, tt.want)
			}
		})
	}
}
