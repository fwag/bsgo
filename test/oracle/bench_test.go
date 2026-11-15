//go:build oracle

package oracle

import (
	"sort"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/solv"
)

// warmCases are the corpus cases that expand successfully; they exercise
// repeated resolution against one long-lived pool.
func warmCases(f *fixture) []oracleCase {
	var out []oracleCase
	for _, tc := range oracleCases {
		if len(runGo(f, tc.conf, tc.deps).errs) == 0 {
			out = append(out, tc)
		}
	}
	return out
}

func resolveOnce(f *fixture, tc oracleCase) error {
	_, err := f.pool.GetBuild(parseConf(tc.conf), []string{"oracletest"}, tc.deps)
	return err
}

// TestWarmResolveLatency requires p95 <= 50 ms for resolves on a warm pool.
func TestWarmResolveLatency(t *testing.T) {
	f := loadFixture(t)
	cases := warmCases(f)
	if len(cases) == 0 {
		t.Fatal("no successful cases")
	}
	const rounds = 20
	var lat []time.Duration
	for r := 0; r < rounds; r++ {
		for _, tc := range cases {
			start := time.Now()
			if err := resolveOnce(f, tc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			lat = append(lat, time.Since(start))
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50, p95, max := lat[len(lat)/2], lat[len(lat)*95/100], lat[len(lat)-1]
	t.Logf("warm resolve over %d samples: p50=%v p95=%v max=%v", len(lat), p50, p95, max)
	if p95 > 50*time.Millisecond {
		t.Errorf("p95 %v exceeds 50ms", p95)
	}
}

// TestColdPoolBuild reports the one-time cost of building the pool.
func TestColdPoolBuild(t *testing.T) {
	f := loadFixture(t)
	blob, err := solv.RPMMDToSolv("oracle", f.primary)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "oracle", Data: blob, Hash: f.hash}})
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	t.Logf("pool from solv blob (%d bytes): %v", len(blob), time.Since(start))
	start = time.Now()
	if _, err := pp.GetBuild(parseConf(baseConf), nil, []string{"gcc", "make"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("first resolve on fresh pool: %v", time.Since(start))
}

func BenchmarkWarmResolve(b *testing.B) {
	f := loadFixture(b)
	cases := warmCases(f)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := resolveOnce(f, cases[i%len(cases)]); err != nil {
			b.Fatal(err)
		}
	}
}
