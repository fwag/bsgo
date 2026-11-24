package manifest

import (
	"strings"
	"testing"
)

func TestResourceLimitsValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      ResourceLimits
		wantErr string
	}{
		{"empty", ResourceLimits{}, ""},
		{"full", ResourceLimits{Memory: "24Gi", VCPUs: 8, Disk: "100Gi", Timeout: "8h"}, ""},
		{"plain numbers", ResourceLimits{Memory: "512", Disk: "50", Timeout: "900"}, ""},
		{"bad memory", ResourceLimits{Memory: "lots"}, "memory"},
		{"zero memory", ResourceLimits{Memory: "0Gi"}, "memory"},
		{"bad disk", ResourceLimits{Disk: "-5Gi"}, "disk"},
		{"bad timeout", ResourceLimits{Timeout: "soon"}, "timeout"},
		{"negative vcpus", ResourceLimits{VCPUs: -1}, "vcpus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err %v, want mention of %q", err, tc.wantErr)
			}
		})
	}
}

func TestResourceLimitsMerge(t *testing.T) {
	base := ResourceLimits{Memory: "4Gi", VCPUs: 2, Disk: "20Gi", Timeout: "2h"}
	tests := []struct {
		name string
		over ResourceLimits
		want ResourceLimits
	}{
		{"empty keeps base", ResourceLimits{}, base},
		{"vcpus and memory", ResourceLimits{Memory: "24Gi", VCPUs: 8}, ResourceLimits{Memory: "24Gi", VCPUs: 8, Disk: "20Gi", Timeout: "2h"}},
		{"all", ResourceLimits{Memory: "1Gi", VCPUs: 1, Disk: "1Gi", Timeout: "1h"}, ResourceLimits{Memory: "1Gi", VCPUs: 1, Disk: "1Gi", Timeout: "1h"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := base.Merge(tc.over); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
