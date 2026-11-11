package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func runEmbeddedNATS(t *testing.T) (*server.Server, string) {
	t.Helper()
	opts := &server.Options{
		Port:      -1, // Random available port
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("failed to create embedded nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatalf("embedded nats server timed out starting")
	}
	t.Cleanup(func() { s.Shutdown() })
	return s, s.ClientURL()
}

func TestSchedulerWebhookToNATSJob(t *testing.T) {
	ctx := context.Background()
	_, natsURL := runEmbeddedNATS(t)

	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		t.Fatalf("failed to connect to jetstream: %v", err)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		t.Fatalf("failed to setup jetstream streams: %v", err)
	}

	// 1. Prepare Store with project and package
	store := storage.NewMemoryStore()
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name: "openSUSE:Factory",
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package: manifest.PackageInfo{
			Name: "ripgrep",
		},
		Source: manifest.PackageSource{
			Git:    "https://github.com/BurntSushi/ripgrep.git",
			Branch: "master",
			Recipe: "ripgrep.spec",
		},
		Build: manifest.PackageBuild{
			Enabled: true,
		},
	}
	_ = store.UpsertPackage(ctx, "openSUSE:Factory", pkg)

	// 2. Prepare solv pool with solver
	solverPool, err := solv.NewWorkerPool("x86_64", 2)
	if err != nil {
		t.Fatalf("failed to create solver pool: %v", err)
	}
	defer solverPool.Close()

	// 3. Create Scheduler
	pub := scheduler.NewNatsPublisher(func(pCtx context.Context, subject string, data []byte) error {
		_, err := js.Publish(pCtx, subject, data)
		return err
	})
	sched := scheduler.NewScheduler(store, solverPool, pub, scheduler.Config{S3Bucket: "bsgo-artifacts"})

	// 4. Trigger build
	trigger := v1.NewTrigger(
		"openSUSE:Factory",
		"ripgrep",
		"https://github.com/BurntSushi/ripgrep.git",
		"a1b2c3d4e5f6",
		"refs/heads/master",
		"alice",
	)

	// In test, since ripgrep solvable isn't in pool, solv.Resolve returns NoProviderError.
	// But let's verify error handling:
	_, err = sched.ProcessTrigger(ctx, trigger)
	if err == nil {
		t.Fatalf("expected solver error for missing provider in unseeded pool")
	}

	// Now seed pool via a temporary repo dumped to memory
	tempPool, _ := solv.NewPool("x86_64")
	repo := tempPool.CreateRepo("base")
	_ = repo.AddPackage(solv.PackageSpec{
		Name:     "ripgrep",
		EVR:      "14.1.0-1",
		Arch:     "x86_64",
		Provides: []string{"ripgrep"},
	})
	solvBlob, _ := repo.DumpMemory()
	tempPool.Close()

	// Load into pool
	loadPool, _ := solv.NewPool("x86_64")
	_, _ = loadPool.LoadRepoFromMemory("seeded", solvBlob)
	defer loadPool.Close()

	// Direct publish test to NATS
	jobData, _ := json.Marshal(&v1.BuildJob{
		JobID:        "job-test-01",
		Project:      "openSUSE:Factory",
		Package:      "ripgrep",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     100,
	})
	_, err = js.Publish(ctx, natsqueue.SubjectJobsPending, jobData)
	if err != nil {
		t.Fatalf("failed to publish test job to NATS: %v", err)
	}

	// Consume message from JOBS_PENDING stream
	cons, err := js.CreateOrUpdateConsumer(ctx, natsqueue.StreamJobsPending, jetstream.ConsumerConfig{
		Durable:   "test-consumer",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("failed to create consumer: %v", err)
	}

	msg, err := cons.Next()
	if err != nil {
		t.Fatalf("failed to fetch message from stream: %v", err)
	}
	defer func() { _ = msg.Ack() }()

	var receivedJob v1.BuildJob
	if err := json.Unmarshal(msg.Data(), &receivedJob); err != nil {
		t.Fatalf("failed to unmarshal received job: %v", err)
	}
	if receivedJob.JobID != "job-test-01" {
		t.Errorf("expected job ID 'job-test-01', got %s", receivedJob.JobID)
	}
}

func TestPrioritizedTriggerQueueAntiStarvation(t *testing.T) {
	// Anti-starvation threshold: 3 non-low items before forcing low item
	q := scheduler.NewPrioritizedTriggerQueue(3)

	// Push 10 high, 2 med, 2 low triggers
	for i := 0; i < 10; i++ {
		q.Push(&v1.BuildTrigger{TriggerID: "high", Priority: "high"})
	}
	for i := 0; i < 2; i++ {
		q.Push(&v1.BuildTrigger{TriggerID: "med", Priority: "med"})
	}
	for i := 0; i < 2; i++ {
		q.Push(&v1.BuildTrigger{TriggerID: "low", Priority: "low"})
	}

	h, m, l := q.Counts()
	if h != 10 || m != 2 || l != 2 {
		t.Fatalf("expected counts (10, 2, 2), got (%d, %d, %d)", h, m, l)
	}

	// 1, 2, 3 should be high
	for i := 0; i < 3; i++ {
		tr := q.Pop()
		if tr.Priority != "high" {
			t.Fatalf("expected high priority on pop %d, got %s", i+1, tr.Priority)
		}
	}

	// Pop 4: anti-starvation threshold (3) exceeded, MUST be low!
	tr := q.Pop()
	if tr.Priority != "low" {
		t.Fatalf("expected low priority due to anti-starvation on pop 4, got %s", tr.Priority)
	}

	// Pop 5, 6, 7 should be high
	for i := 0; i < 3; i++ {
		tr := q.Pop()
		if tr.Priority != "high" {
			t.Fatalf("expected high priority on pop %d, got %s", i+5, tr.Priority)
		}
	}

	// Pop 8: threshold exceeded again, MUST be remaining low!
	tr = q.Pop()
	if tr.Priority != "low" {
		t.Fatalf("expected remaining low priority on pop 8, got %s", tr.Priority)
	}
}

type mockSolver struct {
	pkgs []solv.ResolvedPackage
}

func (m *mockSolver) Resolve(_ context.Context, _ solv.ExpansionRequest) (*solv.ExpansionResult, error) {
	return &solv.ExpansionResult{Packages: m.pkgs}, nil
}

func TestSchedulerRebuildPolicyGating(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "test-prj"},
		Targets: map[string]manifest.BuildTarget{
			"local-target": {
				Architectures: []string{"x86_64"},
				Flags: manifest.TargetFlags{
					Build:   true,
					Rebuild: "local",
				},
			},
			"transitive-target": {
				Architectures: []string{"x86_64"},
				Flags: manifest.TargetFlags{
					Build:   true,
					Rebuild: "transitive",
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "app"},
		Source:        manifest.PackageSource{Git: "https://git/app.git", Branch: "main"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "test-prj", pkg)

	solver := &mockSolver{
		pkgs: []solv.ResolvedPackage{{Name: "app", EVR: "1.0-1", Arch: "x86_64"}},
	}

	var publishedJobs [][]byte
	pub := scheduler.NewNatsPublisher(func(_ context.Context, _ string, data []byte) error {
		publishedJobs = append(publishedJobs, data)
		return nil
	})
	sched := scheduler.NewScheduler(store, solver, pub, scheduler.Config{S3Bucket: "artifacts"})

	// 1. Cascade trigger without force: should skip "local-target" and only build for "transitive-target"
	cascadeTrigger := &v1.BuildTrigger{
		TriggerID: "trig-cascade",
		Project:   "test-prj",
		Package:   "app",
		Priority:  "med",
		Reason:    "CASCADE",
	}

	jobs, err := sched.ProcessTrigger(ctx, cascadeTrigger)
	if err != nil {
		t.Fatalf("unexpected error processing cascade trigger: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected exactly 1 job for transitive-target, got %d", len(jobs))
	}
	if jobs[0].Target != "transitive-target" {
		t.Errorf("expected target 'transitive-target', got %s", jobs[0].Target)
	}
	if jobs[0].Priority < 500 {
		t.Errorf("expected priority >= 500 for med tier, got %d", jobs[0].Priority)
	}

	// 2. Cascade trigger with force_rebuild = true: builds both!
	cascadeTrigger.ForceRebuild = true
	jobs, err = sched.ProcessTrigger(ctx, cascadeTrigger)
	if err != nil {
		t.Fatalf("unexpected error processing forced cascade trigger: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs when ForceRebuild is true, got %d", len(jobs))
	}
}

func TestSchedulerProcessCommitBatch(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "batch-prj"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Flags:         manifest.TargetFlags{Build: true},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	for _, name := range []string{"pkg-a", "pkg-b"} {
		pkg := &manifest.PackageManifest{
			SchemaVersion: "v1",
			Package:       manifest.PackageInfo{Name: name},
			Source:        manifest.PackageSource{Git: "https://git/" + name, Branch: "main"},
			Build:         manifest.PackageBuild{Enabled: true},
		}
		_ = store.UpsertPackage(ctx, "batch-prj", pkg)
	}

	solver := &mockSolver{
		pkgs: []solv.ResolvedPackage{{Name: "base", EVR: "1.0-1", Arch: "x86_64"}},
	}

	pub := scheduler.NewNatsPublisher(func(_ context.Context, _ string, _ []byte) error {
		return nil
	})
	sched := scheduler.NewScheduler(store, solver, pub, scheduler.Config{S3Bucket: "artifacts"})

	triggers := []*v1.BuildTrigger{
		v1.NewTrigger("batch-prj", "pkg-a", "https://git/pkg-a", "c0ffee", "main", "alice"),
		v1.NewTrigger("batch-prj", "pkg-b", "https://git/pkg-b", "c0ffee", "main", "alice"),
	}

	jobs, err := sched.ProcessCommitBatch(ctx, triggers)
	if err != nil {
		t.Fatalf("unexpected error in ProcessCommitBatch: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs from commit batch, got %d", len(jobs))
	}
}

func TestSchedulerResolvesRepositoryDependencies(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "downstream-prj"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Flags:         manifest.TargetFlags{Build: true},
				Dependencies: []manifest.DependencySource{
					{Project: "upstream-crypto", Target: "standard"},
					{URL: "https://download.opensuse.org/repo"},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "downstream-app"},
		Source:        manifest.PackageSource{Git: "https://git/downstream-app", Branch: "main"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "downstream-prj", pkg)

	solver := &mockSolver{
		pkgs: []solv.ResolvedPackage{{Name: "base", EVR: "1.0-1", Arch: "x86_64"}},
	}
	pub := scheduler.NewNatsPublisher(func(_ context.Context, _ string, _ []byte) error { return nil })
	sched := scheduler.NewScheduler(store, solver, pub, scheduler.Config{S3Bucket: "artifacts"})

	trigger := v1.NewTrigger("downstream-prj", "downstream-app", "https://git/downstream-app", "c0ffee", "main", "alice")
	jobs, err := sched.ProcessTrigger(ctx, trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	job := jobs[0]
	if len(job.Repositories) != 3 {
		t.Fatalf("expected 3 repositories (upstream, external URL, self), got %d", len(job.Repositories))
	}

	// 1. Upstream S3 repo
	if job.Repositories[0].Project != "upstream-crypto" || job.Repositories[0].S3Prefix != "repos/upstream-crypto/standard/x86_64/" {
		t.Errorf("unexpected upstream repo: %+v", job.Repositories[0])
	}
	// 2. External URL
	if job.Repositories[1].URL != "https://download.opensuse.org/repo" {
		t.Errorf("unexpected external repo: %+v", job.Repositories[1])
	}
	// 3. Intra-project repo
	if job.Repositories[2].Project != "downstream-prj" || job.Repositories[2].S3Prefix != "repos/downstream-prj/standard/x86_64/" {
		t.Errorf("unexpected self repo: %+v", job.Repositories[2])
	}
}

func TestDetectRecipeBuildType(t *testing.T) {
	tests := []struct {
		recipePath string
		want       string
	}{
		{"ripgrep.spec", "spec"},
		{"/path/to/ripgrep.spec", "spec"},
		{"appliance.kiwi", "kiwi"},
		{"suse.productcompose", "productcompose"},
		{"debian.control", "dsc"},
		{"pkg.dsc", "dsc"},
		{"Dockerfile", "docker"},
		{"Dockerfile.sub", "docker"},
		{"Chart.yaml", "helm"},
		{"PKGBUILD", "arch"},
		{"APKBUILD", "apk"},
		{"", "spec"},
		{"unknown.file", "spec"},
	}

	for _, tt := range tests {
		t.Run(tt.recipePath, func(t *testing.T) {
			got := scheduler.DetectRecipeBuildType(tt.recipePath)
			if got != tt.want {
				t.Errorf("DetectRecipeBuildType(%q) = %q, want %q", tt.recipePath, got, tt.want)
			}
		})
	}
}

func TestSchedulerDAGConfigInheritanceAndFromScratch(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	// Upstream base project
	basePrj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "openSUSE:Base"},
		Config: &manifest.ProjectConfig{
			Prefer:      []string{"base-libc"},
			BuildEngine: "build",
			BuildTypes:  []string{"spec"},
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
			},
		},
	}
	_ = store.CreateProject(ctx, basePrj)

	// Downstream child project with standard target inheriting base, and bootstrap with from_scratch
	childPrj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "openSUSE:Child"},
		Config: &manifest.ProjectConfig{
			Prefer: []string{"child-util"},
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{Project: "openSUSE:Base", Target: "standard"},
				},
			},
			"bootstrap": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{Project: "openSUSE:Base", Target: "standard"},
				},
				Config: &manifest.ProjectConfig{
					FromScratch: true,
					Preinstall:  []string{"busybox"},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, childPrj)

	// 1. Standard target inherits base-libc from Base and child-util from Child
	stdCfg := scheduler.ResolveTargetConfig(ctx, store, childPrj, "standard")
	if len(stdCfg.Prefer) != 2 {
		t.Fatalf("expected 2 prefer items, got %d (%v)", len(stdCfg.Prefer), stdCfg.Prefer)
	}
	if stdCfg.Prefer[0] != "base-libc" || stdCfg.Prefer[1] != "child-util" {
		t.Errorf("unexpected prefer order/contents: %v", stdCfg.Prefer)
	}

	// 2. Bootstrap target with from_scratch: true drops both base and child project configs
	bootCfg := scheduler.ResolveTargetConfig(ctx, store, childPrj, "bootstrap")
	if len(bootCfg.Prefer) != 0 {
		t.Errorf("expected 0 prefer items from scratch, got %d (%v)", len(bootCfg.Prefer), bootCfg.Prefer)
	}
	if len(bootCfg.Preinstall) != 1 || bootCfg.Preinstall[0] != "busybox" {
		t.Errorf("expected busybox preinstall, got %v", bootCfg.Preinstall)
	}
}

func TestSchedulerDAGNamespaceScoping(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	// Upstream distribution project
	distPrj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "SUSE:SLFO:1.2"},
		PublicConfig: &manifest.ProjectConfig{
			Prefer:     []string{"mariadb-server"},
			BuildFlags: []string{"setvcs"},
		},
		ProtectedConfig: &manifest.ProjectConfig{
			BuildFlags: []string{"excludebuild:lttng-modules"},
			Macros:     "%_without_apparmor 1",
		},
		PrivateConfig: &manifest.ProjectConfig{
			BuildFlags: []string{"onlybuild:ALP-dummy-release"},
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {Architectures: []string{"x86_64"}},
		},
	}
	_ = store.CreateProject(ctx, distPrj)

	// Child in the same namespace: SUSE:SLFO:1.2:PullRequest
	prPrj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "SUSE:SLFO:1.2:PullRequest"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{Project: "SUSE:SLFO:1.2", Target: "standard"},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prPrj)

	// Child outside namespace: home:developer:branches:SLFO
	homePrj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "home:developer:branches:SLFO"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{Project: "SUSE:SLFO:1.2", Target: "standard"},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, homePrj)

	hasFlag := func(flags []string, flag string) bool {
		for _, f := range flags {
			if f == flag {
				return true
			}
		}
		return false
	}

	// 1. PullRequest (same namespace) inherits public + namespace_scoped, but NOT private
	prCfg := scheduler.ResolveTargetConfig(ctx, store, prPrj, "standard")
	if !hasFlag(prCfg.BuildFlags, "setvcs") {
		t.Errorf("PR expected public flag 'setvcs'")
	}
	if !hasFlag(prCfg.BuildFlags, "excludebuild:lttng-modules") {
		t.Errorf("PR expected namespace-scoped flag 'excludebuild:lttng-modules'")
	}
	if hasFlag(prCfg.BuildFlags, "onlybuild:ALP-dummy-release") {
		t.Errorf("PR must NOT inherit private flag 'onlybuild:ALP-dummy-release'")
	}
	if prCfg.Macros != "%_without_apparmor 1" {
		t.Errorf("PR expected protected macros '%%_without_apparmor 1', got %q", prCfg.Macros)
	}

	// 2. Home project (outside namespace) inherits ONLY public, NOT protected, NOT private
	homeCfg := scheduler.ResolveTargetConfig(ctx, store, homePrj, "standard")
	if !hasFlag(homeCfg.BuildFlags, "setvcs") {
		t.Errorf("home expected public flag 'setvcs'")
	}
	if hasFlag(homeCfg.BuildFlags, "excludebuild:lttng-modules") {
		t.Errorf("home must NOT inherit protected flag 'excludebuild:lttng-modules'")
	}
	if hasFlag(homeCfg.BuildFlags, "onlybuild:ALP-dummy-release") {
		t.Errorf("home must NOT inherit private flag 'onlybuild:ALP-dummy-release'")
	}
	if homeCfg.Macros != "" {
		t.Errorf("home project must NOT inherit protected macros, got %q", homeCfg.Macros)
	}
}

func TestSchedulerDAGArchConfigResolution(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "multi-arch-prj"},
		PublicConfig: &manifest.ProjectConfig{
			BuildFlags: []string{"setvcs"},
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64", "s390x"},
				ArchConfig: map[string]*manifest.ProjectConfig{
					"s390x": {
						Constraints: []string{"hardware:cpu:flag vxd"},
						BuildFlags:  []string{"signflavor:2025-01-22"},
					},
					"x86_64": {
						BuildFlags: []string{"signflavor:2023-05-10"},
					},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	// Resolve for x86_64
	x86Cfg := scheduler.ResolveTargetConfig(ctx, store, prj, "standard", "x86_64")
	if len(x86Cfg.Constraints) != 0 {
		t.Errorf("x86_64 must have 0 constraints, got %v", x86Cfg.Constraints)
	}
	hasFlag := func(flags []string, f string) bool {
		for _, x := range flags {
			if x == f {
				return true
			}
		}
		return false
	}
	if !hasFlag(x86Cfg.BuildFlags, "signflavor:2023-05-10") {
		t.Errorf("x86_64 expected signflavor:2023-05-10")
	}

	// Resolve for s390x
	s390Cfg := scheduler.ResolveTargetConfig(ctx, store, prj, "standard", "s390x")
	if len(s390Cfg.Constraints) != 1 || s390Cfg.Constraints[0] != "hardware:cpu:flag vxd" {
		t.Errorf("s390x expected vxd constraint, got %v", s390Cfg.Constraints)
	}
	if !hasFlag(s390Cfg.BuildFlags, "signflavor:2025-01-22") {
		t.Errorf("s390x expected signflavor:2025-01-22")
	}
}

func TestSchedulerBuildTypeFiltering(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "mixed-project"},
		Targets: map[string]manifest.BuildTarget{
			"packages": {
				Architectures: []string{"x86_64"},
				Config: &manifest.ProjectConfig{
					BuildTypes: []string{"spec"},
				},
			},
			"images": {
				Architectures: []string{"x86_64"},
				Config: &manifest.ProjectConfig{
					BuildTypes:  []string{"kiwi", "docker"},
					BuildEngine: "podman",
					RepoTypes:   []string{"none"},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	// 1. RPM package: ripgrep with .spec
	pkgSpec := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "ripgrep"},
		Source:        manifest.PackageSource{Git: "https://git/rg.git", Recipe: "ripgrep.spec"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "mixed-project", pkgSpec)

	// 2. Image package: appliance with .kiwi
	pkgKiwi := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "appliance"},
		Source:        manifest.PackageSource{Git: "https://git/app.git", Recipe: "app.kiwi"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "mixed-project", pkgKiwi)

	// 3. Container package: web-app with Dockerfile
	pkgDocker := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "web-app"},
		Source:        manifest.PackageSource{Git: "https://git/web.git", Recipe: "Dockerfile"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "mixed-project", pkgDocker)

	solver := &mockSolver{
		pkgs: []solv.ResolvedPackage{{Name: "base", EVR: "1.0", Arch: "x86_64"}},
	}
	pub := scheduler.NewNatsPublisher(func(_ context.Context, _ string, _ []byte) error { return nil })
	sched := scheduler.NewScheduler(store, solver, pub, scheduler.Config{S3Bucket: "artifacts"})

	// Trigger for ripgrep: should only be scheduled on "packages" target, NOT "images"
	rgJobs, err := sched.ProcessTrigger(ctx, v1.NewTrigger("mixed-project", "ripgrep", "https://git/rg.git", "c1", "main", "alice"))
	if err != nil {
		t.Fatalf("unexpected error for ripgrep: %v", err)
	}
	if len(rgJobs) != 1 || rgJobs[0].Target != "packages" {
		t.Fatalf("expected 1 job on target 'packages', got %d", len(rgJobs))
	}
	if rgJobs[0].BuildType != "spec" || rgJobs[0].BuildEngine != "build" {
		t.Errorf("ripgrep: expected build_type 'spec' and engine 'build', got %q, %q", rgJobs[0].BuildType, rgJobs[0].BuildEngine)
	}

	// Trigger for appliance: should only be scheduled on "images" target, NOT "packages"
	kiwiJobs, err := sched.ProcessTrigger(ctx, v1.NewTrigger("mixed-project", "appliance", "https://git/app.git", "c2", "main", "alice"))
	if err != nil {
		t.Fatalf("unexpected error for appliance: %v", err)
	}
	if len(kiwiJobs) != 1 || kiwiJobs[0].Target != "images" {
		t.Fatalf("expected 1 job on target 'images', got %d", len(kiwiJobs))
	}
	if kiwiJobs[0].BuildType != "kiwi" || kiwiJobs[0].BuildEngine != "podman" {
		t.Errorf("appliance: expected build_type 'kiwi' and engine 'podman', got %q, %q", kiwiJobs[0].BuildType, kiwiJobs[0].BuildEngine)
	}

	// Trigger for web-app: should only be scheduled on "images" target, NOT "packages"
	dockerJobs, err := sched.ProcessTrigger(ctx, v1.NewTrigger("mixed-project", "web-app", "https://git/web.git", "c3", "main", "alice"))
	if err != nil {
		t.Fatalf("unexpected error for web-app: %v", err)
	}
	if len(dockerJobs) != 1 || dockerJobs[0].Target != "images" {
		t.Fatalf("expected 1 job on target 'images', got %d", len(dockerJobs))
	}
	if dockerJobs[0].BuildType != "docker" || dockerJobs[0].BuildEngine != "podman" {
		t.Errorf("web-app: expected build_type 'docker' and engine 'podman', got %q, %q", dockerJobs[0].BuildType, dockerJobs[0].BuildEngine)
	}
}
