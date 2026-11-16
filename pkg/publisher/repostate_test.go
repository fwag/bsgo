package publisher_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestPublishRecordsRepositoryState(t *testing.T) {
	tests := []struct {
		name      string
		pol       publisher.Policy
		build     bool
		breakIt   func(h *harness)
		wantErr   bool
		wantState string // after the publish; empty means no row
		wantHash  bool
		during    string // state seen while createrepo runs; empty means it never runs
	}{
		{"successful publish", publishAll, true, nil, false, storage.RepoPublished, true, storage.RepoPublishing},
		{"publishing disabled is unpublished", publisher.Policy{RPMMD: true}, true, nil, false, storage.RepoUnpublished, false, ""},
		{"empty build repository leaves the state", publishAll, false, nil, false, "", false, ""},
		{"failed publish is finished with the reason", publishAll, true,
			func(h *harness) { h.cr.err = errors.New("boom") }, true, storage.RepoFinished, true, storage.RepoPublishing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			states := storage.NewMemoryStore()
			h := newHarness(t, tt.pol)
			h.pub.States = states
			if tt.build {
				h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
			}
			if tt.breakIt != nil {
				tt.breakIt(h)
			}
			seen := ""
			h.cr.gate = func() {
				if st, err := states.GetPrpaState(ctx, testPrpa.Project, testPrpa.Repo, testPrpa.Arch); err == nil {
					seen = st.State
				}
			}
			_, err := h.pub.Publish(ctx, testPrpa)
			if (err != nil) != tt.wantErr {
				t.Fatalf("publish error %v, want error=%v", err, tt.wantErr)
			}
			if seen != tt.during {
				t.Errorf("state during publish %q, want %q", seen, tt.during)
			}
			st, gerr := states.GetPrpaState(ctx, testPrpa.Project, testPrpa.Repo, testPrpa.Arch)
			if tt.wantState == "" {
				if !errors.Is(gerr, storage.ErrNotFound) {
					t.Errorf("want no row, got %+v, %v", st, gerr)
				}
				return
			}
			if gerr != nil || st.State != tt.wantState || (st.IndexHash != "") != tt.wantHash {
				t.Errorf("row %+v, %v; want %s hash=%v", st, gerr, tt.wantState, tt.wantHash)
			}
			if tt.wantErr && st.Details == "" {
				t.Error("a failed publish must record the reason")
			}
		})
	}
}
