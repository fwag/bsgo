package main

import (
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
)

func TestResourcesOf(t *testing.T) {
	tests := []struct {
		name    string
		in      manifest.ResourceLimits
		vcpus   int32
		mem     int64
		disk    int64
		timeout int32
		wantErr string
	}{
		{"defaults", manifest.ResourceLimits{}, 2, 4096, 20, 0, ""},
		{"manifest values", manifest.ResourceLimits{Memory: "32Gi", VCPUs: 16, Disk: "100Gi", Timeout: "8h"}, 16, 32768, 100, 28800, ""},
		{"mebibytes and plain numbers", manifest.ResourceLimits{Memory: "512Mi", Disk: "50", Timeout: "900"}, 2, 512, 50, 900, ""},
		{"decimal suffixes", manifest.ResourceLimits{Memory: "8G", Disk: "1T"}, 2, 8192, 1024, 0, ""},
		{"bad memory", manifest.ResourceLimits{Memory: "lots"}, 0, 0, 0, 0, "memory"},
		{"negative disk", manifest.ResourceLimits{Disk: "-5Gi"}, 0, 0, 0, 0, "disk"},
		{"bad timeout", manifest.ResourceLimits{Timeout: "soon"}, 0, 0, 0, 0, "timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := resourcesOf(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want mention of %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.VCPUs != tc.vcpus || r.MemoryMB != tc.mem || r.DiskGB != tc.disk || r.TimeoutSeconds != tc.timeout {
				t.Errorf("got %+v", r)
			}
		})
	}
}

func TestJobFromSpec(t *testing.T) {
	spec := &checker.JobSpec{
		JobID: "j1", Project: "prj", Repo: "standard", Arch: "x86_64",
		Package: "systemd:mini", BasePackage: "systemd", Flavor: "mini",
		SourceURL: "https://git.example/systemd", SourceCommit: strings.Repeat("a", 40), RecipeFile: "systemd.spec",
		BuildType: "spec", Config: "Release: <CI_CNT>.<B_CNT>\n", Archpath: "x86_64:noarch",
		Release: "3.2", DistURL: "bsgo://prj/standard/aaaa-systemd:mini", Meta: "m", Priority: 77, LogIdleLimit: 600,
		Bdeps: []checker.Bdep{
			{Name: "glibc", Version: "2.38", Release: "1", Arch: "x86_64", SHA256: "s1", Preinstall: true,
				Source: checker.BdepSource{S3Key: "artifacts/prj/standard/x86_64/glibc/glibc-2.38-1.x86_64.rpm"}},
			{Name: "gcc", Epoch: "1", Version: "13", Release: "2", Arch: "x86_64", SHA256: "s2", NotMeta: true,
				Source: checker.BdepSource{URL: "https://mirror.example/repo/x86_64/gcc-13-2.x86_64.rpm"}},
		},
	}
	job, err := jobFromSpec(spec, "bucket", time.Unix(5, 0))
	if err != nil {
		t.Fatal(err)
	}
	bi := job.BuildInfo
	if bi == nil || bi.ObsPackage != "systemd" || bi.Flavor != "mini" || bi.SourceCommit != spec.SourceCommit ||
		bi.Release != "3.2" || bi.Meta != "m" || bi.LogIdleLimit != 600 || bi.ArchPath != "x86_64:noarch" {
		t.Fatalf("buildinfo %+v", bi)
	}
	if job.Package != "systemd:mini" || job.Target != "standard" || job.Priority != 77 {
		t.Errorf("job %+v", job)
	}
	if job.Destination.S3Prefix != "artifacts/prj/standard/x86_64/systemd:mini/" || job.Destination.S3Bucket != "bucket" {
		t.Errorf("destination %+v", job.Destination)
	}
	if len(bi.Bdeps) != 2 || !bi.Bdeps[0].Preinstall || !bi.Bdeps[1].NotMeta || bi.Bdeps[1].Epoch != "1" {
		t.Errorf("bdeps %+v", bi.Bdeps)
	}
}

func TestJobFromSpecRejectsBadBdepSources(t *testing.T) {
	tests := map[string]checker.BdepSource{
		"neither":      {},
		"both":         {S3Key: "k", URL: "https://x/y.rpm"},
		"relative url": {URL: "x86_64/y.rpm"},
		"ftp":          {URL: "ftp://x/y.rpm"},
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			spec := &checker.JobSpec{Package: "p", Bdeps: []checker.Bdep{{Name: "b", Source: src}}}
			if _, err := jobFromSpec(spec, "b", time.Now()); err == nil {
				t.Error("want an error")
			}
		})
	}
}
