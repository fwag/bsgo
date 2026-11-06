# PRD: Build Service Go (bsgo)

Status: ready-for-agent

## Problem Statement

For over twenty years, the Open Build Service (OBS) backend has served as the backbone for building and releasing Linux distributions. However, its legacy architecture has accumulated severe architectural limitations that prevent modern scalability and developer velocity:
1. **Coupled Monolith & Scalability Bottlenecks**: The legacy backend relies on single-threaded Perl daemons (`bs_sched`, `bs_dispatch`) and shared POSIX filesystems (NFS/CIFS) for state and binary storage. This creates I/O contention, high scheduling latency, and makes horizontal autoscaling in Kubernetes impossible.
2. **Proprietary Version Control & Impedance Matching**: OBS maintains its own bespoke VCS (`bs_srcserver`) using uncompressed CPIO archives and sequential revisions. Integrating Git has required complex, fragile synchronization shims (`scmsync`, `obs-scm-bridge`) that convert Git commits into legacy OBS formats.
3. **Severe Build Latency for Developers**: Large rebuild cascades frequently stall because leaf packages starve critical-path dependencies (like `glibc` or `gcc`), and workers suffer cold-cache penalties by re-downloading hundreds of prerequisite packages over the network.
4. **Poor Fit for Cloud-Native & AI Software Factories**: The legacy XML API and `osc` CLI tool were designed exclusively for human terminal operators, lacking deterministic machine-readable output, self-documenting introspection, and fine-grained token scoping required for automated CI pipelines and autonomous AI agents.

## Solution

`bsgo` is a clean-slate, cloud-native reimplementation of the Open Build Service backend in Go, engineered specifically for Kubernetes, S3-compatible object storage, NATS JetStream messaging, and multi-architecture build workers.

Key architectural pillars:
- **Git-Native Source of Truth**: All package recipes, sources, and patches live directly in standard Git repositories (with Git LFS for large binary archives). Project manifests are declaratively managed in Git and partitioned using 2-character hex consistent hash sharding (`packages/xx/<package>.yaml`) to support distributions with over 16,000 packages without merge conflicts or tree bloat.
- **Pure S3 Storage**: All compiled binaries, published RPM repositories (`repomd.xml`), OCI container images, and build logs are stored in S3-compatible object storage, completely eliminating shared POSIX/NFS storage requirements.
- **In-Process SAT Dependency Solving**: PostgreSQL houses relational metadata while in-process CGO bindings to openSUSE's `libsolv` evaluate pre-compiled `.solv` memory blobs via a deterministic greedy expander, resolving complex dependency graphs in under a millisecond.
- **High-Throughput Scheduler-Dispatcher Architecture**: Schedulers publish resolved build jobs to NATS JetStream `WorkQueue` streams. A dedicated Go Dispatcher prioritizes jobs by topological dependency depth, matches multi-dimensional hardware constraints, and routes jobs with warm-cache affinity directly to worker nodes.
- **Multi-Architecture Worker Fleet Isolation**: Lightweight Go worker daemons (`bsgo-worker`) run across diverse hardware architectures (x86_64, aarch64, s390x, ppc64le), bootstrapping sterile QEMU/KVM virtual machines with disposable copy-on-write QCOW2 overlays to execute `/usr/lib/build/build`.
- **Defense-in-Depth Security**: The Kubernetes control plane runs under gVisor (`runtimeClassName: gvisor`). User and agent authentication is handled via Authentik OIDC (supporting OAuth2 Device Flow for humans and fine-grained, resource-scoped Personal Access Tokens for AI software factory agents).
- **Dual-Mode CLI & AI Software Factory Readiness**: The `bsgo` CLI provides an interactive Charm `bubbletea`/`lipgloss` TUI dashboard for developers, while emitting strictly deterministic, unadorned JSON on `stdout` when invoked with `--json` for CI and AI agents.

## User Stories

### Packagers and Distribution Maintainers
1. As a distribution maintainer, I want to define project build targets and base dependency layers in a declarative `project.yaml` in Git, so that distribution configurations are version-controlled and auditable through Git pull requests.
2. As a package maintainer, I want each package to reside in its own Git repository with Git LFS tracking large source archives, so that repository cloning remains lightweight and standard Git tooling can be used.
3. As a distribution release engineer, I want package manifests in project repositories to be partitioned into 256 hash shards (`packages/xx/<package>.yaml`), so that thousands of concurrent package updates avoid Git merge conflicts and directory tree bloat.
4. As a packager, I want to specify floating Git branch tracking for development builds or exact commit SHAs for release builds, so that I can balance automated continuous integration with immutable release reproducibility.
5. As a packager, I want to declare hardware resource requirements (RAM, vCPUs, disk, timeout) directly within my package manifest, so that resource-intensive packages like Chromium or LLVM receive adequate worker allocations.
6. As a packager, I want to enable or disable builds per target architecture within the package manifest, so that packages are not compiled on unsupported architectures.
7. As a distribution maintainer, I want the CLI to automatically compute hash shard paths and make signed Git commits via the API when updating package resources, so that I do not need to manually browse shard folders or manage Git clones.

### Build Performance, Scheduling, and Dispatching
8. As a distribution maintainer, I want build jobs to be prioritized by topological dependency depth, so that critical-path blockers (e.g. `glibc`, `gcc`) compile first and unblock downstream package cascades.
9. As a developer, I want build jobs to be assigned to workers that already have prerequisite RPMs cached locally, so that chroot bootstrapping takes seconds rather than minutes.
10. As a packager, I want build jobs superseded by a newer Git commit to be automatically aborted on the worker, so that worker capacity is immediately reclaimed and not wasted on obsolete binaries.
11. As a scheduler, I want to resolve package dependencies against in-memory `libsolv` binary pools in sub-millisecond execution times, so that repository dependency calculation never bottlenecks the build pipeline.
12. As a scheduler, I want dependency resolution to follow a deterministic greedy expander that halts with explicit ambiguity errors when multiple providers exist, so that build chroots are strictly reproducible over time.
13. As a developer, I want to stream live build logs with sub-second latency from an active worker, so that I can monitor compilation progress in real time.
14. As an operator, I want live log streaming to use ephemeral pub/sub messaging that drops packets when no observer is connected, so that unmonitored builds consume zero persistent disk or queue storage.

### Infrastructure and Worker Execution
15. As a system administrator, I want build workers to run as standalone Go daemons on our existing PXE-booted bare metal fleet (including s390x and PowerPC), so that we do not have to deploy Kubernetes onto incompatible hardware.
16. As a security engineer, I want every build job to execute inside a sterile QEMU/KVM virtual machine with a disposable QCOW2 overlay, so that untrusted build code cannot escape or pollute subsequent builds.
17. As an operator, I want workers to pull jobs with an acknowledgment lease, so that if a worker host loses power or crashes, the job is automatically re-delivered to another healthy node.
18. As a worker, I want to receive a deterministic build job specification containing pre-resolved S3 RPM URLs and checksums, so that the worker does not require database access or dependency resolution logic.
19. As an operator, I want build workers to upload completed RPMs and final build logs directly to S3, so that the control plane does not act as a high-bandwidth file transfer proxy.

### AI Software Factory Agents and Automation
20. As an AI agent, I want every CLI command to support a `--json` flag emitting pure, deterministic JSON on `stdout` without ANSI escape codes, so that I can reliably parse outputs without terminal stripping.
21. As an AI agent, I want all operational errors and diagnostics to be routed strictly to `stderr`, so that stderr can be inspected independently of structured data on stdout.
22. As an AI agent, I want every command to offer comprehensive, self-documenting `--help` text with explicit intent and copy-pasteable examples, so that I can discover and reason about tools autonomously.
23. As an AI agent, I want all system state to be mutable via non-interactive CLI commands with `--yes`/`-y` flags, so that I never encounter interactive prompt dead ends.
24. As an AI software factory orchestrator, I want to issue Personal Access Tokens with fine-grained, resource-scoped permissions (e.g. `project:write:home:bot`), so that autonomous agents cannot exceed their designated project blast radius.
25. As a CI runner, I want CLI commands to support `--dry-run`, so that I can validate manifest changes and permissions without triggering real build jobs.

### Security, Authentication, and Governance
26. As an organization, I want users to authenticate against our internal Authentik instance via OIDC Device Authorization Grant (`bsgo auth login`), so that corporate SSO and MFA policies are enforced.
27. As a security architect, I want the `bsgo` API service to statelessly verify Authentik JWT tokens using JWKS without maintaining a local password database, so that credentials management is centralized.
28. As an administrator, I want authenticated users to be provisioned into PostgreSQL just-in-time, so that audit logs and build records maintain referential integrity with real user records.
29. As a project maintainer, I want full operational authority over my project without an artificial admin/maintainer distinction, so that access control remains simple and intuitive.
30. As a security architect, I want all control plane microservices to run under gVisor in Kubernetes, so that container breakout vulnerabilities are mitigated by kernel syscall sandboxing.

### Repository Publishing and Artifact Consumption
31. As a consumer, I want published RPM repositories to be hosted directly on S3 and consumable via HTTPS by standard package managers (`zypper`, `dnf`), so that software installation requires no proprietary client tools.
32. As a repository publisher, I want incremental metadata generation (`createrepo_c --update --recycle-pkglist`) to download only existing XML metadata and newly built RPMs, so that publications do not require downloading gigabytes of historical binaries.
33. As a repository publisher, I want repository updates to be serialized per `(project, target, arch)` using NATS queues and PostgreSQL advisory locks, so that concurrent builds never corrupt repository metadata.
34. As a consumer, I want repository metadata updates to follow an atomic "commit-by-pointer" protocol on S3 where `repomd.xml` is uploaded last, so that clients never observe partial or inconsistent repository states.
35. As a container developer, I want `bsgo` to publish built container images directly to target OCI registries with Cosign signatures, so that images are immediately pullable by Podman and Kubernetes.

## Implementation Decisions

### 1. Service Architecture & Microservice Boundaries
The `bsgo` control plane is decomposed into discrete, single-purpose Go microservices:
- **`bsgo-api`**: Stateless API gateway providing REST and gRPC endpoints, validating Authentik JWTs, handling JIT user provisioning, validating YAML manifests against JSON schemas, and executing signed Git commits for manifest mutations.
- **`bsgo-scheduler`**: Event-driven engine listening for Git triggers and completed builds. Embeds openSUSE `libsolv` via CGO with pinned OS threads (`runtime.LockOSThread()`) to execute deterministic solvable expansions against in-memory `.solv` caches loaded from PostgreSQL via `fmemopen()`. Emits resolved `BuildJob` records.
- **`bsgo-dispatcher`**: Fleet orchestrator maintaining real-time worker capacity and cache state. Evaluates multi-dimensional hardware constraints (RAM, vCPUs, sandbox type), sorts jobs by topological dependency depth, applies warm-cache worker affinity scoring, and delivers jobs directly to worker inboxes. Emits abort control signals for superseded commits.
- **`bsgo-worker`**: Standalone Go daemon executing on bare-metal PXE hosts or KubeVirt VM pods. Prepares input directories, pulls deterministic chroot RPMs from S3, manages disposable QCOW2 overlays on base images, invokes `/usr/lib/build/build`, streams live log chunks via Core NATS, and uploads finished artifacts and statistics to S3.
- **`bsgo-publisher`**: Repository maintenance worker consuming publication events from NATS JetStream. Performs metadata-only S3 syncs, runs `createrepo_c` with incremental flags under PostgreSQL advisory locks, and atomically updates `repomd.xml` on S3.

### 2. Storage & State Partitioning
- **Git Repositories (The Canonical Spec)**: Houses declarative `project.yaml` and sharded `packages/xx/<package>.yaml` manifests. Git's Merkle tree provides immutable auditability and PR review workflows.
- **PostgreSQL (The Operational Projection & Access Control)**: Stores projected project/package records for sub-millisecond scheduler queries, pre-compiled binary `.solv` cache blobs (`BYTEA`), user identities, Authentik group mappings, personal access tokens, and project-level ACLs.
- **S3-Compatible Object Store (Artifacts & Repodata)**: Houses all compiled binary RPMs, published repository XML metadata (`repodata/`), OCI container blobs, and complete build log archives.
- **NATS JetStream (Durable Message Bus)**: Provides persistent, durable streams for inbound triggers (`TRIGGERS`), unassigned jobs (`JOBS_PENDING`), job lifecycle audits (`JOB_STATUS`), and publication requests (`PUBLISH`).
- **Core NATS (Zero-Overhead Ephemeral Telemetry)**: Carries worker heartbeats (`bsgo.workers.heartbeat`), direct worker job dispatch (`bsgo.workers.<id>.job`), superseded build cancellation (`bsgo.jobs.<id>.control`), and live stdout/stderr chunk streaming (`bsgo.logs.<id>`).

### 3. Manifest Schemas & Hash Sharding
- **Hash Sharding**: Package manifests are stored at `packages/<hash2>/<package>.yaml`, where `<hash2>` is `sha256(<package_name>)[:2]`, guaranteeing balanced distribution across 256 subdirectories.
- **Root Manifest (`project.yaml`)**:
  ```yaml
  schema_version: "v1"
  project:
    name: "openSUSE:Factory"
    title: "openSUSE Factory Distribution"
  targets:
    standard:
      architectures: ["x86_64", "aarch64", "ppc64le", "s390x"]
      dependencies:
        - project: "openSUSE:Factory:Staging"
          target: "standard"
        - url: "https://download.opensuse.org/repositories/openSUSE:/Tools/openSUSE_Factory/"
          type: "rpm-md"
      flags:
        build: true
        publish: true
        debuginfo: true
  ```
- **Package Manifest (`packages/xx/<package>.yaml`)**:
  ```yaml
  schema_version: "v1"
  package:
    name: "chromium"
  source:
    git: "https://github.com/openSUSE/chromium.git"
    branch: "factory"
    # commit: "8f7e2a9b..." # Optional exact pin
  build:
    enabled: true
    targets:
      standard: true
    architectures:
      exclude: ["s390x"]
  resources:
    memory: "32Gi"
    vcpus: 16
    disk: "100Gi"
    timeout: "8h"
  ```

### 4. Protocol Buffer & Wire Contracts
All inter-service messaging contracts are strictly defined in Protocol Buffers (`syntax = "proto3"`) with ProtoJSON transcoding:
- `BuildTrigger`: Carries project, package, git URL, commit SHA, and trigger source.
- `BuildJob`: Immutable bill of materials containing source commit, exact pre-resolved chroot RPM list (filename, S3 URL, SHA-256), hardware constraints, and S3 artifact destination.
- `WorkerHeartbeat`: Telemetry emitted every 10–15s reporting worker ID, architecture, capacity, active job, and local SSD cached package hashes.
- `JobControl`: Abort command dispatched to active workers when a build is superseded by a newer commit.
- `JobStatusUpdate`: Reports phase transitions (`FETCHING_SOURCES`, `BUILDING`, `SUCCEEDED`, `FAILED`, `UNCHANGED`), exit codes, execution timing statistics, and output artifact metadata.
- `PublishRequest`: Requests repository metadata regeneration for `(project, target, arch)`.

### 5. Access Control & Authentik IAM Integration
- **Authentication**: Authentik OIDC supporting Device Authorization Flow for interactive CLI users, and hashed Personal Access Tokens (PAT) for AI agents and CI.
- **JIT User Provisioning**: Authentik JWTs automatically populate/update the PostgreSQL `users` table upon authenticated requests.
- **Fine-Grained Agent Scopes**: Tokens enforce resource-level scopes in the format `<action>:<resource>[:<id>]` (e.g. `project:write:home:ai-agent`, `build:trigger:openSUSE:Factory`).
- **Role Hierarchy**:
  - `is_superuser`: Universal authority across all projects and workers.
  - `project_maintainer`: Complete operational authority over a specific project. Personal forks (`home:<user>`) automatically make the creator maintainer.
  - `project_reader`: Read permission for private projects.
- **Package Maintainers**: Explicitly descoped from `bsgo`; package source code and review ownership belong strictly to external Git forges.

### 6. Reviewability & Cognitive Ergonomics (The evergit Standard)
All Go code must comply with `.golangci.yml` derived from `evergit`:
- **Short Functions (`funlen`)**: Maximum 80 lines and 50 statements per function.
- **Shallow Nesting (`nestif`)**: Maximum conditional nesting depth of 5.
- **Cognitive Simplicity (`gocognit`)**: Cognitive complexity score under 20.
- **Zero Duplicate Code (`dupl`)**: Threshold 100 tokens.

## Testing Decisions

### What Makes a Good Test
Tests must verify **external behavior across public API boundaries**, never private state or internal implementation details:
- A solver test asserts that given a solvable repository and package dependencies, `Resolve()` produces the expected minimal chroot package list, or returns a typed error (`ErrNoProvider`, `ErrChoice`).
- A dispatcher test asserts that given a set of queued jobs with varying priorities and constraints, `Dispatch()` assigns the critical-path job to the qualified worker with the highest warm-cache score.
- A CLI test asserts that invoking a command with `--json` outputs schema-conformant JSON on stdout and zero text on stderr, while an invalid flag returns a non-zero exit code and structured error on stderr.
- **No Mocking Frameworks**: Tests use real in-memory data structures, SQLite/PostgreSQL instances, or embedded NATS servers rather than mock object reflection.

### Primary Testing Seams
1. **End-to-End System Seam (`test/e2e`)**:
   - The highest-level seam: CLI commands invoking the full API Gateway $\to$ Scheduler $\to$ Dispatcher $\to$ Worker loop.
   - Backed by containerized integration test dependencies using Testcontainers for Go (real PostgreSQL, real NATS JetStream, real Garage S3).
2. **Deterministic Solver Seam (`pkg/solv`)**:
   - Tests CGO `libsolv` integration: populating binary `.solv` pools, verifying deterministic greedy expansion, enforcing project `prefer`/`ignore` rules, and detecting dependency cycles.
3. **Dispatcher & Priority Seam (`pkg/dispatcher`)**:
   - Tests priority queue ordering (topological depth), hardware constraint validation, cache affinity scoring, worker lease timeouts, and superseded commit abort signals.
4. **Publisher & Repodata Seam (`pkg/publisher`)**:
   - Tests incremental repodata generation with `createrepo_c`, S3 atomic commit-by-pointer, and PostgreSQL advisory locking.
5. **CLI & Formatting Seam (`cmd/bsgo`, `prototype/cli`)**:
   - Tests dual-mode execution: Bubble Tea TUI rendering and keybindings, deterministic JSON output fidelity, and error isolation to stderr.

### Prior Art in Codebase
- The runnable prototype in `prototype/cli/` demonstrates the testing pattern (`tui_test.go`), verifying view state transitions and keyboard updates in under 5ms.
- OpenSUSE `perl-BSSolv` test suites (`testdata/`) provide established test cases for dependency cycles, solvable expansion, and conflict resolution.

## Out of Scope

The following systems and features are explicitly ruled out of scope for `bsgo`:
1. **Legacy WebUI and Frontend**: No Ruby on Rails or HTML frontend. User interaction is handled via modern Git forges (Gitea/GitHub/GitLab) and the `bsgo` CLI.
2. **Legacy XML API & `osc` Protocol Emulation**: No backward compatibility shims for legacy XML endpoints. `bsgo` provides a clean-slate REST/gRPC API.
3. **Server-Side VCS & Merge Request Engine**: `bsgo` does not store CPIO archives, track custom revision numbers, or manage code review diffs. Git forges own version control.
4. **Package-Level Maintainers**: Package ownership and contribution workflows belong in the external Git forge. `bsgo` operates strictly on project-level context.
5. **Built-in Email & Bugzilla Notification Engine**: Notification routing and Bugzilla updates are descoped from core `bsgo` and delegated to external webhook consumers.
6. **Early Build Preemption**: Killing or suspending active builds to make room for higher-priority jobs is deferred out of scope to avoid thrashing large compilations; only builds superseded by new commits are aborted.
7. **Maintenance Incident & QAM Workflows**: Removed in favor of standard Git branch and tag promotion pipelines.

## Further Notes

### Future Graduation Paths (Not Yet Specified on Map)
- **Automated Artifact & Repodata Signing**: Integrating Sigstore/Cosign for container images and project-specific GPG keyrings for RPM repodata.
- **Product Composer Integration**: Workflow integration with `product-composer` to generate unified installation media and disk images.
- **KubeVirt Autoscaling Engine**: Dynamic auto-provisioning of ephemeral worker VM instances in Kubernetes when bare-metal worker queues become backlogged.
- **Worker Fleet PXE / Systemd Deployment**: Standardized packaging and deployment automation for running `bsgo-worker` on existing physical infrastructure.
