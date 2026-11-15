package obsimport_test

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/obsimport"
)

// TestVerifyReportsMismatch proves the gate can fail: a manifest that lost a
// statement or changed a define must be reported.
func TestVerifyReportsMismatch(t *testing.T) {
	fx := loadFixture(t, "SUSE_SLFO_1.2")
	fx.manifest.PublicConfig.Prefer = fx.manifest.PublicConfig.Prefer[1:]
	fx.manifest.PublicConfig.Defines[0].Value = "changed"
	mismatches, err := obsimport.Verify(fx.manifest, fx.prjconf, arches)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) == 0 {
		t.Fatal("a modified manifest passed the round-trip gate")
	}
}
