package signer_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/signer"
)

func TestEnabled(t *testing.T) {
	tests := []struct {
		name         string
		noAutoCreate bool
		keyed        []string
		project      string
		want         bool
	}{
		{"auto create signs everything", false, nil, "home:bot", true},
		{"own key", true, []string{"home:bot"}, "home:bot", true},
		{"inherited key", true, []string{"home"}, "home:bot:sub", true},
		{"no key and no creation", true, nil, "home:bot", false},
		{"key of a sibling does not count", true, []string{"home:other"}, "home:bot", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, signer.Config{}, "home", "home:bot", "home:bot:sub", "home:other")
			for _, k := range tc.keyed {
				if _, err := f.signer.ResolveKey(ctx, k, ""); err != nil {
					t.Fatal(err)
				}
			}
			s := signer.New(f.daemon, f.store, signer.Config{NoAutoCreate: tc.noAutoCreate})
			got, err := s.Enabled(ctx, tc.project, "")
			if err != nil || got != tc.want {
				t.Fatalf("Enabled = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
