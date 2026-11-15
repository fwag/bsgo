package publisher

import (
	"encoding/json"
	"fmt"
	"strings"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// ValidateProvenance verifies that an in-toto SLSA provenance statement is valid
// and that its declared artifact SHA-256 digests match the built RPMs.
func ValidateProvenance(provenanceData []byte, expectedRPMs map[string]string) (*v1.InTotoStatement, error) {
	var stmt v1.InTotoStatement
	if err := json.Unmarshal(provenanceData, &stmt); err != nil {
		return nil, fmt.Errorf("invalid provenance json: %w", err)
	}

	if stmt.Type != v1.InTotoStatementType {
		return nil, fmt.Errorf("invalid in-toto statement type: %q, expected %q", stmt.Type, v1.InTotoStatementType)
	}
	if stmt.PredicateType != v1.SLSAProvenanceType {
		return nil, fmt.Errorf("invalid slsa predicate type: %q, expected %q", stmt.PredicateType, v1.SLSAProvenanceType)
	}

	for _, sub := range stmt.Subject {
		expectedDigest, exists := expectedRPMs[sub.Name]
		if exists && expectedDigest != "" {
			actualDigest := sub.Digest["sha256"]
			if !strings.EqualFold(actualDigest, expectedDigest) {
				return nil, fmt.Errorf("provenance digest mismatch for %s: expected %s, got %s", sub.Name, expectedDigest, actualDigest)
			}
		}
	}

	return &stmt, nil
}
