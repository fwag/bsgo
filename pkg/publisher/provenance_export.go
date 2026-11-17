package publisher

import (
	"context"
	"fmt"
	"path"
	"strings"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

const (
	// provenanceArtifact is the name of the signed SLSA provenance of a unit
	// in the artifact prefix, next to its binaries (written by the signing
	// stage).
	provenanceArtifact = "_slsa_provenance.json"
	// provenanceSuffix replaces ".rpm" in the published name, as in OBS
	// bs_publish: foo-1.0-1.x86_64.rpm gets foo-1.0-1.x86_64.slsa_provenance.json
	// next to it in the repository.
	provenanceSuffix = ".slsa_provenance.json"
)

// provenanceName is the published provenance name of a binary.
func provenanceName(rpm string) string {
	return strings.TrimSuffix(rpm, ".rpm") + provenanceSuffix
}

// exportProvenance copies the signed provenance of each transferred binary
// next to it in the repository and returns the names written. A provenance
// is exported only when it is a DSSE envelope whose statement lists the
// binary with its published sha256; anything else (a pre-signing unsigned
// statement, a provenance of other bytes) is not published, and a copy left
// by an earlier publish is removed. The signature itself is not checked here,
// the publisher holds no keys; consumers verify it with the project key.
func (p *Publisher) exportProvenance(ctx context.Context, prefix string, bins []Binary, listing *Listing, transferred []string) ([]string, error) {
	fresh := make(map[string]bool, len(transferred))
	for _, n := range transferred {
		fresh[n] = true
	}
	var written []string
	for _, b := range bins {
		if !fresh[b.Filename] {
			continue
		}
		name := provenanceName(b.Filename)
		ok, src, err := p.exportable(ctx, b)
		if err != nil {
			return written, err
		}
		if !ok {
			if _, stale := listing.Objects[name]; stale {
				if err := p.Store.DeleteObject(ctx, prefix+name); err != nil {
					return written, fmt.Errorf("deleting %s: %w", prefix+name, err)
				}
			}
			continue
		}
		if err := p.Store.CopyObject(ctx, src, prefix+name); err != nil {
			return written, fmt.Errorf("publishing %s: %w", name, err)
		}
		written = append(written, name)
	}
	return written, nil
}

// exportable finds the provenance next to the binary's artifact and reports
// whether it is a signed statement that covers the binary.
func (p *Publisher) exportable(ctx context.Context, b Binary) (bool, string, error) {
	src := path.Join(path.Dir(b.Source), provenanceArtifact)
	objs, err := p.Store.ListObjects(ctx, src)
	if err != nil {
		return false, "", fmt.Errorf("listing %s: %w", src, err)
	}
	found := false
	for _, o := range objs {
		found = found || o.Key == src
	}
	if !found {
		return false, "", nil
	}
	raw, err := p.Store.ReadObject(ctx, src)
	if err != nil {
		return false, "", fmt.Errorf("reading %s: %w", src, err)
	}
	return coversBinary(raw, b), src, nil
}

// coversBinary reports whether data is a DSSE envelope with a signature
// whose in-toto statement has b as a subject with b's sha256.
func coversBinary(data []byte, b Binary) bool {
	env, ok := v1.ParseDSSEEnvelope(data)
	if !ok || len(env.Signatures) == 0 {
		return false
	}
	st, err := env.Statement()
	if err != nil || st.Type != v1.InTotoStatementType || st.PredicateType != v1.SLSAProvenanceType {
		return false
	}
	for _, s := range st.Subject {
		if s.Name == b.Filename {
			return strings.EqualFold(s.Digest["sha256"], b.SHA256)
		}
	}
	return false
}
