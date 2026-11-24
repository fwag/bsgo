package obsimport

import (
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

func TestOverridesResources(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		want      manifest.ResourceLimits
		wantUnset bool
		wantErr   string
	}{
		{name: "full", yaml: "packages:\n  a:\n    resources: {memory: 24Gi, vcpus: 8, disk: 100Gi, timeout: 12h}\n",
			want: manifest.ResourceLimits{Memory: "24Gi", VCPUs: 8, Disk: "100Gi", Timeout: "12h"}},
		{name: "partial keeps the rest empty", yaml: "packages:\n  a:\n    resources: {vcpus: 8}\n",
			want: manifest.ResourceLimits{VCPUs: 8}},
		{name: "sandbox only", yaml: "packages:\n  a:\n    build: {constraints: {sandbox: kvm}}\n"},
		{name: "bad memory", yaml: "packages:\n  a:\n    resources: {memory: lots}\n", wantErr: "resources.memory"},
		{name: "bad timeout", yaml: "packages:\n  a:\n    resources: {timeout: soon}\n", wantErr: "resources.timeout"},
		{name: "negative vcpus", yaml: "packages:\n  a:\n    resources: {vcpus: -2}\n", wantErr: "resources.vcpus"},
		{name: "unknown resource key", yaml: "packages:\n  a:\n    resources: {cpus: 2}\n", wantErr: "cpus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := ParseOverrides([]byte(tc.yaml))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want mention of %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pkg := &manifest.PackageManifest{}
			pkg.Package.Name = "a"
			applied, unused := o.Apply([]*manifest.PackageManifest{pkg})
			if applied != 1 || len(unused) != 0 {
				t.Fatalf("applied %d unused %v", applied, unused)
			}
			if pkg.Resources != tc.want {
				t.Errorf("resources %+v, want %+v", pkg.Resources, tc.want)
			}
		})
	}
}
