package storage_test

import (
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/storagetest"
)

func TestMemoryStoreRevisions(t *testing.T) {
	storagetest.Revisions(t, storage.NewMemoryStore(), "mem", nil)
}

func TestResolveVrev(t *testing.T) {
	rev := func(seq int, state, version string, vrev int) storage.PackageRevision {
		return storage.PackageRevision{Seq: seq, State: state, Version: version, Vrev: vrev}
	}
	res, skip, pend := storage.RevisionResolved, storage.RevisionSkipped, storage.RevisionPending
	tests := []struct {
		name    string
		earlier []storage.PackageRevision
		version string
		git     int
		min     int
		want    int
	}{
		{"no history", nil, "1", 25, 0, 25},
		{"floor wins over a lower git count", []storage.PackageRevision{rev(1, res, "1", 10)}, "1", 4, 0, 11},
		{"git wins over the floor", []storage.PackageRevision{rev(1, res, "1", 10)}, "1", 40, 0, 40},
		{"floor is the highest of the same version", []storage.PackageRevision{rev(1, res, "1", 10), rev(2, res, "2", 3), rev(3, res, "1", 12)}, "1", 1, 0, 13},
		{"skipped and pending are ignored", []storage.PackageRevision{rev(1, skip, "1", 10), rev(2, pend, "1", 0)}, "1", 2, 0, 2},
		{"minimum", nil, "1", 2, 9, 9},
		{"known to unknown", []storage.PackageRevision{rev(1, res, "1", 8)}, storage.UnknownVersion, 1, 0, 9},
		{"unknown to known", []storage.PackageRevision{rev(1, res, storage.UnknownVersion, 8)}, "1", 1, 0, 9},
		{"unknown rule uses the previous revision only", []storage.PackageRevision{rev(1, res, "1", 20), rev(2, res, "2", 3)}, storage.UnknownVersion, 1, 0, 4},
		{"two known versions restart", []storage.PackageRevision{rev(1, res, "1", 8)}, "2", 1, 0, 1},
	}
	for _, tc := range tests {
		if got := storage.ResolveVrev(tc.earlier, tc.version, tc.git, tc.min); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRevisionBackoff(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if got := storage.RevisionBackoff(i + 1); got != w {
			t.Errorf("attempt %d: %s, want %s", i+1, got, w)
		}
	}
	if got := storage.RevisionBackoff(0); got != time.Minute {
		t.Errorf("attempt 0: %s", got)
	}
}
