package signer_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/signer"
)

func TestAutoExtendDisabledOrFresh(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		cfg  signer.Config
	}{
		{"fresh key with default margin", signer.Config{}},
		{"short lived key with autoextend disabled", signer.Config{KeyDays: 10, ExtendBefore: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.cfg, "p")
			for range 2 {
				if _, err := f.signer.ResolveKey(ctx, "p", ""); err != nil {
					t.Fatal(err)
				}
			}
			if f.daemon.extensions != 0 || f.daemon.generated != 1 {
				t.Errorf("extensions %d generated %d", f.daemon.extensions, f.daemon.generated)
			}
		})
	}
}
