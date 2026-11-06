package main

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	successStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575"))

	highlightStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00BFFF"))

	stepStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFA500"))

	subtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888"))
)

func main() {
	ctx := context.Background()

	fmt.Println()
	fmt.Println(headerStyle.Render(" bsgo ") + " " + highlightStyle.Render("Multi-Project Dependency & Cache Affinity Demo"))
	fmt.Println(subtleStyle.Render("Demonstrating cross-project solving, critical-path priority, and warm-cache affinity."))
	fmt.Println()

	setupProjects(ctx)
	solveDependencies()
	runDispatchSimulation(ctx)

	fmt.Println(successStyle.Render("✔ Demo completed successfully!"))
	fmt.Println()
}

func setupProjects(ctx context.Context) {
	fmt.Println(stepStyle.Render("Step 1: Declaring Two Interdependent Projects in Git/Storage"))
	store := storage.NewMemoryStore()

	prjBase := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "base:core", Title: "Base Core System Libraries"},
		Targets:       map[string]manifest.BuildTarget{"standard": {Architectures: []string{"x86_64"}}},
	}
	_ = store.CreateProject(ctx, prjBase)
	_ = store.UpsertPackage(ctx, "base:core", &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "libgreeting"},
		Source:        manifest.PackageSource{Git: "https://github.com/openSUSE/libgreeting.git", Branch: "main"},
		Build:         manifest.PackageBuild{Enabled: true},
	})
	fmt.Printf("  • Created Project %s (package: %s)\n", highlightStyle.Render("base:core"), successStyle.Render("libgreeting"))

	prjApps := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "apps:tools", Title: "User Applications and Tools"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies:  []manifest.DependencySource{{Project: "base:core", Target: "standard"}},
			},
		},
	}
	_ = store.CreateProject(ctx, prjApps)
	_ = store.UpsertPackage(ctx, "apps:tools", &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "hello-world"},
		Source:        manifest.PackageSource{Git: "https://github.com/openSUSE/hello-world.git", Branch: "main"},
		Build:         manifest.PackageBuild{Enabled: true},
	})
	fmt.Printf("  • Created Project %s (package: %s, depends on: %s)\n\n",
		highlightStyle.Render("apps:tools"), successStyle.Render("hello-world"), highlightStyle.Render("base:core"))
}

func solveDependencies() {
	fmt.Println(stepStyle.Render("Step 2: Resolving Dependencies across Projects with In-Process libsolv"))
	pool, _ := solv.NewPool("x86_64")
	defer pool.Close()

	repoBase := pool.CreateRepo("base:core")
	_ = repoBase.AddPackage(solv.PackageSpec{
		Name: "glibc", EVR: "2.38-1", Arch: "x86_64", Provides: []string{"libc.so.6()(64bit)"},
	})
	_ = repoBase.AddPackage(solv.PackageSpec{
		Name: "libgreeting", EVR: "1.0.0-1", Arch: "x86_64",
		Provides: []string{"libgreeting.so.1()(64bit)", "libgreeting-devel"},
		Requires: []string{"libc.so.6()(64bit)"},
	})

	repoApps := pool.CreateRepo("apps:tools")
	_ = repoApps.AddPackage(solv.PackageSpec{
		Name: "hello-world", EVR: "0.1.0-1", Arch: "x86_64",
		Provides: []string{"hello-world"},
		Requires: []string{"libgreeting-devel", "libc.so.6()(64bit)"},
	})

	startSolve := time.Now()
	res, _ := pool.Resolve(solv.ExpansionRequest{BuildRequires: []string{"hello-world"}})
	solveDuration := time.Since(startSolve)

	fmt.Printf("  • %s resolved in %v (Tarjan sequenced order):\n", successStyle.Render("✔ libsolv"), solveDuration)
	for i, p := range res.Packages {
		fmt.Printf("      %d. %s-%s.%s\n", i+1, p.Name, p.EVR, p.Arch)
	}
	fmt.Println()
}

func runDispatchSimulation(ctx context.Context) {
	fmt.Println(stepStyle.Render("Step 3: Registering Worker Fleet & Enqueueing Jobs"))
	disp := dispatcher.NewDispatcher(dispatcher.NewMockPublisher(nil), 30*time.Second)
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:        "worker-cloud-01",
		Architecture:    "x86_64",
		Status:          "IDLE",
		Hardware:        v1.WorkerHardware{TotalVCPUs: 8, TotalMemoryMB: 16384, AvailableDiskGB: 50, SandboxType: "kvm"},
		CachedRPMHashes: []string{"hash-glibc"},
		TimestampUnix:   time.Now().Unix(),
	})

	disp.EnqueueJob(&v1.BuildJob{
		JobID: "job-apps-hello-world", Project: "apps:tools", Package: "hello-world",
		Architecture: "x86_64", Priority: 25,
		ChrootRPMs: []v1.ChrootRPM{{Filename: "glibc.rpm", SHA256: "hash-glibc"}, {Filename: "libgreeting.rpm", SHA256: "hash-libgreeting"}},
	})
	disp.EnqueueJob(&v1.BuildJob{
		JobID: "job-base-libgreeting", Project: "base:core", Package: "libgreeting",
		Architecture: "x86_64", Priority: 100, // Critical-path blocker
		ChrootRPMs: []v1.ChrootRPM{{Filename: "glibc.rpm", SHA256: "hash-glibc"}},
	})

	fmt.Println(stepStyle.Render("Step 4: Dispatcher Round 1 — Critical-Path Prioritization"))
	w1, j1, _ := disp.DispatchNext(ctx)
	fmt.Printf("  • Dispatcher chose: %s (Priority: %d) -> Assigned to %s\n",
		successStyle.Render(j1.Project+"/"+j1.Package), j1.Priority, highlightStyle.Render(w1))

	fmt.Println(stepStyle.Render("Step 5: Worker Finishes libgreeting & Dispatcher Round 2 — Warm-Cache Affinity"))
	disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
		WorkerID:        "worker-cloud-01",
		Architecture:    "x86_64",
		Status:          "IDLE",
		CachedRPMHashes: []string{"hash-glibc", "hash-libgreeting"},
		Hardware:        v1.WorkerHardware{TotalVCPUs: 8, TotalMemoryMB: 16384, AvailableDiskGB: 50, SandboxType: "kvm"},
		TimestampUnix:   time.Now().Unix(),
	})

	w2, j2, _ := disp.DispatchNext(ctx)
	fmt.Printf("  • Dispatcher chose: %s -> Assigned to %s (Cache Affinity: 100%%)\n\n",
		successStyle.Render(j2.Project+"/"+j2.Package), highlightStyle.Render(w2))
}
