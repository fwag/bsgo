package poc_test

import (
	"context"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestInterdependentProjectsEndToEndPoC(t *testing.T) {
	ctx := context.Background()

	// 1. Initialize Storage
	store := storage.NewMemoryStore()

	// Project 1: base:core (Provides the base libraries)
	prjBase := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name:  "base:core",
			Title: "Base Core System Libraries",
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
			},
		},
	}
	_ = store.CreateProject(ctx, prjBase)

	pkgLibGreeting := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package: manifest.PackageInfo{
			Name: "libgreeting",
		},
		Source: manifest.PackageSource{
			Git:    "https://github.com/openSUSE/libgreeting.git",
			Branch: "main",
		},
		Build: manifest.PackageBuild{
			Enabled: true,
		},
		Resources: manifest.ResourceLimits{
			Memory: "4Gi",
			VCPUs:  2,
		},
	}
	_ = store.UpsertPackage(ctx, "base:core", pkgLibGreeting)

	// Project 2: apps:tools (Depends on base:core!)
	prjApps := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name:  "apps:tools",
			Title: "User Applications and Tools",
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{
						Project: "base:core",
						Target:  "standard",
					},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prjApps)

	pkgHelloWorld := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package: manifest.PackageInfo{
			Name: "hello-world",
		},
		Source: manifest.PackageSource{
			Git:    "https://github.com/openSUSE/hello-world.git",
			Branch: "main",
		},
		Build: manifest.PackageBuild{
			Enabled: true,
		},
		Resources: manifest.ResourceLimits{
			Memory: "2Gi",
			VCPUs:  2,
		},
	}
	_ = store.UpsertPackage(ctx, "apps:tools", pkgHelloWorld)

	// 2. Setup in-process libsolv solver with solvables
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed to init libsolv pool: %v", err)
	}
	defer pool.Close()

	repoBase := pool.CreateRepo("base:core")
	_ = repoBase.AddPackage(solv.PackageSpec{
		Name:     "glibc",
		EVR:      "2.38-1",
		Arch:     "x86_64",
		Provides: []string{"libc.so.6()(64bit)"},
	})
	_ = repoBase.AddPackage(solv.PackageSpec{
		Name:     "libgreeting",
		EVR:      "1.0.0-1",
		Arch:     "x86_64",
		Provides: []string{"libgreeting.so.1()(64bit)", "libgreeting-devel"},
		Requires: []string{"libc.so.6()(64bit)"},
	})

	repoApps := pool.CreateRepo("apps:tools")
	_ = repoApps.AddPackage(solv.PackageSpec{
		Name:     "hello-world",
		EVR:      "0.1.0-1",
		Arch:     "x86_64",
		Provides: []string{"hello-world"},
		Requires: []string{"libgreeting-devel", "libc.so.6()(64bit)"},
	})

	// Resolve hello-world -> must resolve both libgreeting and glibc!
	res, err := pool.Resolve(solv.ExpansionRequest{
		BuildRequires: []string{"hello-world"},
	})
	if err != nil {
		t.Fatalf("solver failed to resolve across projects: %v", err)
	}
	if len(res.Packages) != 3 {
		t.Fatalf("expected 3 packages (glibc, libgreeting, hello-world), got %d", len(res.Packages))
	}

	// 3. Initialize Dispatcher and register 1 idle worker
	disp := dispatcher.NewDispatcher(dispatcher.NewMockPublisher(nil), 30*time.Second)
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:     "worker-cloud-01",
		Architecture: "x86_64",
		Status:       "IDLE",
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      8,
			TotalMemoryMB:   16384,
			AvailableDiskGB: 50,
			SandboxType:     "kvm",
		},
		CachedRPMHashes: []string{"hash-glibc"},
		TimestampUnix:   time.Now().Unix(),
	})

	// 4. Enqueue both jobs into Dispatcher
	// libgreeting has higher priority (Priority: 100) because hello-world depends on it!
	jobLibGreeting := &v1.BuildJob{
		JobID:        "job-base-libgreeting",
		Project:      "base:core",
		Package:      "libgreeting",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     100, // Blocker priority
		ChrootRPMs: []v1.ChrootRPM{
			{Filename: "glibc-2.38-1.x86_64.rpm", SHA256: "hash-glibc"},
		},
		Resources: v1.ResourceRequirements{
			MemoryMB: 4096,
			VCPUs:    2,
		},
	}

	jobHelloWorld := &v1.BuildJob{
		JobID:        "job-apps-hello-world",
		Project:      "apps:tools",
		Package:      "hello-world",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     25, // Consumer priority
		ChrootRPMs: []v1.ChrootRPM{
			{Filename: "glibc-2.38-1.x86_64.rpm", SHA256: "hash-glibc"},
			{Filename: "libgreeting-1.0.0-1.x86_64.rpm", SHA256: "hash-libgreeting"},
		},
		Resources: v1.ResourceRequirements{
			MemoryMB: 2048,
			VCPUs:    2,
		},
	}

	disp.EnqueueJob(jobHelloWorld)
	disp.EnqueueJob(jobLibGreeting)

	// 5. Verify Critical-Path Prioritization:
	// Even though hello-world was queued first, libgreeting MUST be dispatched first!
	assignedWorker, dispatchedJob, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if dispatchedJob.JobID != "job-base-libgreeting" {
		t.Fatalf("expected critical-path blocker libgreeting to be dispatched first, got %s", dispatchedJob.JobID)
	}
	if assignedWorker != "worker-cloud-01" {
		t.Errorf("expected worker-cloud-01, got %s", assignedWorker)
	}

	// 6. Worker completes libgreeting, builds artifact hash-libgreeting, and reports back
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:        "worker-cloud-01",
		Architecture:    "x86_64",
		Status:          "IDLE", // Idle again
		CachedRPMHashes: []string{"hash-glibc", "hash-libgreeting"},
		Hardware: v1.WorkerHardware{
			TotalVCPUs:      8,
			TotalMemoryMB:   16384,
			AvailableDiskGB: 50,
			SandboxType:     "kvm",
		},
		TimestampUnix: time.Now().Unix(),
	})

	// 7. Verify Warm-Cache Affinity:
	// hello-world is now dispatched to the exact same worker with 100% warm-cache score!
	w2, j2, err := disp.DispatchNext(ctx)
	if err != nil {
		t.Fatalf("dispatch error for hello-world: %v", err)
	}
	if j2.JobID != "job-apps-hello-world" {
		t.Fatalf("expected job-apps-hello-world, got %s", j2.JobID)
	}
	if w2 != "worker-cloud-01" {
		t.Errorf("expected worker-cloud-01, got %s", w2)
	}
}
