package main

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

type Project struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Targets      []string `json:"targets"`
	PackageCount int      `json:"package_count"`
}

type Package struct {
	Project   string          `json:"project"`
	Name      string          `json:"name"`
	Shard     string          `json:"shard"`
	GitURL    string          `json:"git_url"`
	Branch    string          `json:"branch"`
	Commit    string          `json:"commit"`
	Enabled   bool            `json:"enabled"`
	Resources ResourceSpec    `json:"resources"`
	Overrides map[string]bool `json:"overrides,omitempty"`
}

type ResourceSpec struct {
	Memory  string `json:"memory"`
	VCPUs   int    `json:"vcpus"`
	Disk    string `json:"disk"`
	Timeout string `json:"timeout"`
}

type BuildJob struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Package   string    `json:"package"`
	Target    string    `json:"target"`
	Arch      string    `json:"arch"`
	Priority  int       `json:"priority"`
	Status    string    `json:"status"`
	WorkerID  string    `json:"worker_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Duration  string    `json:"duration"`
}

type Worker struct {
	ID        string `json:"id"`
	Arch      string `json:"arch"`
	Status    string `json:"status"`
	VCPUs     int    `json:"vcpus"`
	MemoryMB  int    `json:"memory_mb"`
	ActiveJob string `json:"active_job,omitempty"`
}

func computeShard(pkg string) string {
	h := sha256.Sum256([]byte(pkg))
	return hex.EncodeToString(h[:1]) // 2 hex chars
}

var mockProjects = []Project{
	{
		Name:         "openSUSE:Factory",
		Title:        "openSUSE Factory Distribution",
		Targets:      []string{"standard", "images"},
		PackageCount: 15892,
	},
	{
		Name:         "devel:languages:go",
		Title:        "Go Programming Language Tools",
		Targets:      []string{"openSUSE_Tumbleweed", "SLE_15_SP5"},
		PackageCount: 342,
	},
}

var mockPackages = map[string]Package{
	"openSUSE:Factory/chromium": {
		Project: "openSUSE:Factory",
		Name:    "chromium",
		Shard:   computeShard("chromium"),
		GitURL:  "https://github.com/openSUSE/chromium.git",
		Branch:  "factory",
		Commit:  "8f7e2a9b4c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f",
		Enabled: true,
		Resources: ResourceSpec{
			Memory:  "32Gi",
			VCPUs:   16,
			Disk:    "100Gi",
			Timeout: "8h",
		},
	},
	"openSUSE:Factory/glibc": {
		Project: "openSUSE:Factory",
		Name:    "glibc",
		Shard:   computeShard("glibc"),
		GitURL:  "https://github.com/openSUSE/glibc.git",
		Branch:  "factory",
		Commit:  "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
		Enabled: true,
		Resources: ResourceSpec{
			Memory:  "8Gi",
			VCPUs:   8,
			Disk:    "30Gi",
			Timeout: "3h",
		},
	},
	"openSUSE:Factory/ripgrep": {
		Project: "openSUSE:Factory",
		Name:    "ripgrep",
		Shard:   computeShard("ripgrep"),
		GitURL:  "https://github.com/openSUSE/ripgrep.git",
		Branch:  "main",
		Commit:  "4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f",
		Enabled: true,
		Resources: ResourceSpec{
			Memory:  "4Gi",
			VCPUs:   4,
			Disk:    "20Gi",
			Timeout: "1h",
		},
	},
}

var mockJobs = []BuildJob{
	{
		ID:        "job-9a8b1c",
		Project:   "openSUSE:Factory",
		Package:   "glibc",
		Target:    "standard",
		Arch:      "x86_64",
		Priority:  100, // Critical-path blocker
		Status:    "BUILDING",
		WorkerID:  "worker-x86-01",
		StartedAt: time.Now().Add(-18 * time.Minute),
		Duration:  "18m 22s",
	},
	{
		ID:        "job-7f6e5d",
		Project:   "openSUSE:Factory",
		Package:   "chromium",
		Target:    "standard",
		Arch:      "x86_64",
		Priority:  10,
		Status:    "BUILDING",
		WorkerID:  "worker-x86-02",
		StartedAt: time.Now().Add(-42 * time.Minute),
		Duration:  "42m 05s",
	},
	{
		ID:        "job-3c2b1a",
		Project:   "openSUSE:Factory",
		Package:   "ripgrep",
		Target:    "standard",
		Arch:      "aarch64",
		Priority:  25,
		Status:    "SUCCEEDED",
		WorkerID:  "worker-arm-01",
		StartedAt: time.Now().Add(-3 * time.Minute),
		Duration:  "2m 45s",
	},
	{
		ID:        "job-4d5e6f",
		Project:   "openSUSE:Factory",
		Package:   "kernel-default",
		Target:    "standard",
		Arch:      "s390x",
		Priority:  90,
		Status:    "PENDING",
		StartedAt: time.Now(),
		Duration:  "queued",
	},
}

var mockWorkers = []Worker{
	{
		ID:        "worker-x86-01",
		Arch:      "x86_64",
		Status:    "BUSY",
		VCPUs:     32,
		MemoryMB:  131072,
		ActiveJob: "job-9a8b1c (glibc)",
	},
	{
		ID:        "worker-x86-02",
		Arch:      "x86_64",
		Status:    "BUSY",
		VCPUs:     16,
		MemoryMB:  65536,
		ActiveJob: "job-7f6e5d (chromium)",
	},
	{
		ID:       "worker-arm-01",
		Arch:     "aarch64",
		Status:   "IDLE",
		VCPUs:    16,
		MemoryMB: 65536,
	},
	{
		ID:       "worker-s390x-01",
		Arch:     "s390x",
		Status:   "IDLE",
		VCPUs:    8,
		MemoryMB: 32768,
	},
}
