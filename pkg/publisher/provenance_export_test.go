package publisher_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
)

// provenanceOf returns the provenance file of a unit as the signing stage
// leaves it: a DSSE envelope (signed set) or the worker's bare statement,
// listing the given binaries with the given sha256 override.
func provenanceOf(t *testing.T, signed bool, sums map[string]string, rpms ...rpmSpec) []byte {
	t.Helper()
	st := &v1.InTotoStatement{Type: v1.InTotoStatementType, PredicateType: v1.SLSAProvenanceType}
	for _, r := range rpms {
		sum := r.info().SHA256
		if o, ok := sums[r.filename()]; ok {
			sum = o
		}
		st.Subject = append(st.Subject, v1.SubjectArtifact{Name: r.filename(), Digest: map[string]string{"sha256": sum}})
	}
	payload, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !signed {
		return payload
	}
	out, err := json.Marshal(v1.NewDSSEEnvelope(payload, []byte("signature"), "FP"))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPublishExportsSignedProvenance(t *testing.T) {
	bash := rpmx("bash", "5.2")
	const exported = "bash-5.2-1.1.x86_64.slsa_provenance.json"
	tests := []struct {
		name string
		prov []byte // nil: the unit has no provenance
		want bool
	}{
		{"signed envelope covering the binary", provenanceOf(t, true, nil, bash), true},
		{"unsigned statement of the worker", provenanceOf(t, false, nil, bash), false},
		{"envelope of other bytes", provenanceOf(t, true, map[string]string{bash.filename(): "00"}, bash), false},
		{"envelope without the binary", provenanceOf(t, true, nil, rpmx("curl", "8.0")), false},
		{"no provenance", nil, false},
		{"garbage", []byte("nope"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, publishAll)
			h.idx.builds(t, h.store, testPrpa, "bash", bash)
			if tt.prov != nil {
				h.store.Set(buildrepo.ArtifactPrefix(testPrpa, "bash")+"_slsa_provenance.json", tt.prov)
			}
			h.publish(t)
			if got := h.store.Has(stdPrefix + exported); got != tt.want {
				t.Fatalf("exported = %v, want %v", got, tt.want)
			}
			if tt.want {
				data, err := h.store.ReadObject(context.Background(), stdPrefix+exported)
				if err != nil || !reflect.DeepEqual(data, tt.prov) {
					t.Errorf("exported content differs from the artifact: %v", err)
				}
			}
		})
	}
}

func TestPublishProvenanceFollowsBinary(t *testing.T) {
	h := newHarness(t, publishAll)
	key := buildrepo.ArtifactPrefix(testPrpa, "bash") + "_slsa_provenance.json"
	v52, v53 := rpmx("bash", "5.2"), rpmx("bash", "5.3")
	h.idx.builds(t, h.store, testPrpa, "bash", v52)
	h.store.Set(key, provenanceOf(t, true, nil, v52))
	res := h.publish(t)
	if got := res.SubRepos[publisher.SubRepoStandard].Provenance; !reflect.DeepEqual(got, []string{"bash-5.2-1.1.x86_64.slsa_provenance.json"}) {
		t.Fatalf("first publish exported %v", got)
	}

	// Republishing unchanged binaries does not copy again.
	h.store.TakeOps()
	if got := h.publish(t).SubRepos[publisher.SubRepoStandard].Provenance; len(got) != 0 {
		t.Errorf("unchanged publish exported %v", got)
	}

	// A rebuild replaces binary and provenance; the old provenance goes away.
	h.idx.builds(t, h.store, testPrpa, "bash", v53)
	h.store.Set(key, provenanceOf(t, true, nil, v53))
	res = h.publish(t)
	if got := res.SubRepos[publisher.SubRepoStandard].Provenance; !reflect.DeepEqual(got, []string{"bash-5.3-1.1.x86_64.slsa_provenance.json"}) {
		t.Errorf("rebuild exported %v", got)
	}
	if h.store.Has(stdPrefix + "bash-5.2-1.1.x86_64.slsa_provenance.json") {
		t.Error("provenance of the removed binary is still published")
	}
	if !h.store.Has(stdPrefix + "bash-5.3-1.1.x86_64.slsa_provenance.json") {
		t.Error("provenance of the new binary is missing")
	}
}

func TestPublishDropsProvenanceThatNoLongerCoversBinary(t *testing.T) {
	h := newHarness(t, publishAll)
	key := buildrepo.ArtifactPrefix(testPrpa, "bash") + "_slsa_provenance.json"
	bash := rpmx("bash", "5.2")
	h.idx.builds(t, h.store, testPrpa, "bash", bash)
	h.store.Set(key, provenanceOf(t, true, nil, bash))
	h.publish(t)

	// Same file name, new bytes (resigned RPM) and only an unsigned statement.
	changed := rpmx("bash", "5.2")
	changed.content = "other bytes"
	h.idx.builds(t, h.store, testPrpa, "bash", changed)
	h.store.Set(key, provenanceOf(t, false, nil, changed))
	h.publish(t)
	if h.store.Has(stdPrefix + "bash-5.2-1.1.x86_64.slsa_provenance.json") {
		t.Error("stale provenance of the old bytes is still published")
	}
}
