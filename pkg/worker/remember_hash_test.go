package worker

import (
	"fmt"
	"reflect"
	"testing"
)

func TestRememberHash(t *testing.T) {
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("h%d", i)
		}
		return out
	}
	tests := []struct {
		name    string
		have    []string
		add     []string
		want    []string
		wantLen int
	}{
		{name: "appends", have: nil, add: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "duplicate moves to the end", have: []string{"a", "b"}, add: []string{"a"}, want: []string{"b", "a"}},
		{name: "bounded, oldest dropped", have: many(maxCachedHashes), add: []string{"new"}, wantLen: maxCachedHashes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WorkerDaemon{cachedHashes: tt.have}
			for _, h := range tt.add {
				w.rememberHash(h)
			}
			if tt.want != nil && !reflect.DeepEqual(w.cachedHashes, tt.want) {
				t.Errorf("got %v, want %v", w.cachedHashes, tt.want)
			}
			if tt.wantLen != 0 {
				if len(w.cachedHashes) != tt.wantLen {
					t.Fatalf("len %d, want %d", len(w.cachedHashes), tt.wantLen)
				}
				if w.cachedHashes[0] != "h1" || w.cachedHashes[tt.wantLen-1] != "new" {
					t.Errorf("window is [%s .. %s], want [h1 .. new]", w.cachedHashes[0], w.cachedHashes[tt.wantLen-1])
				}
			}
		})
	}
}
