# Map: OBS Golang Backend (bsgo)

Labels: `wayfinder:map`

## Destination

Deliver an architectural specification, core schemas, and prototype contracts for `bsgo`—a cloud-native, Git-backed Go rewrite of the Open Build Service backend engineered for Kubernetes, S3 artifact storage, NATS JetStream orchestration, and high-performance multi-architecture build workers.

## Notes

- Domain: Cloud-native package build system, distribution packaging (RPM, OCI, Kiwi), dependency resolution.
- Key skills: `domain-modeling`, `grilling`, `research`, `prototype`.
- AI Software Factory Guardrails ([`AGENTS.md`](../../AGENTS.md)):
  - Question 20-year-old OBS legacy decisions (human-in-the-loop if scalability, security, or maintainability are impacted).
  - Use strictly actively maintained, battle-tested third-party libraries.
  - Mandatory pipeline gates: `golangci-lint`, `govulncheck`, `gosec`, table-driven unit tests, containerized integration tests.
- Technical mandates:
  - Git is the sole source of truth (Git LFS for large binaries).
  - S3 for all binary artifacts, repodata, and OCI image layers.
  - PostgreSQL for dependency metadata, in-process `libsolv` for scheduler.
  - NATS JetStream for durable queues (`jobs.<arch>`).
  - Workers run as standalone Go daemons (`bsgo-worker`) invoking `obs-build` inside disposable QEMU/KVM overlays.
  - Control plane runs in Kubernetes under gVisor.

## Decisions so far

- [ADR 0001: Git-Native Package Model and Hash Sharding](../../docs/adr/0001-git-native-package-model-and-hash-sharding.md): Use 2-char hex consistent hash sharding (`packages/xx/<package>.yaml`) in Git project repos for 16k+ packages.
- [ADR 0002: S3-Compatible Storage for Artifacts and Repodata](../../docs/adr/0002-s3-artifact-store-and-repodata-publishing.md): Pure S3 storage for artifacts, logs, and published RPM repodata, removing POSIX NFS.
- [ADR 0003: PostgreSQL for Metadata and In-Process libsolv](../../docs/adr/0003-postgres-metadata-and-in-process-libsolv.md): Relational store for package headers; SAT solver bound via CGO directly in the Go scheduler.
- [ADR 0004: NATS JetStream and Pull-Based Workers](../../docs/adr/0004-nats-jetstream-and-multi-arch-pull-workers.md): Multi-arch architecture queues pulled by lightweight Go worker daemons on existing hardware.
- [ADR 0005: gVisor Control Plane and QEMU/KVM Isolation](../../docs/adr/0005-gvisor-control-plane-and-qemu-kvm-worker-isolation.md): gVisor sandboxing for control plane; full hardware QEMU/KVM with QCOW2 overlays for untrusted builds.
- [ADR 0006: GitOps Project Declarations with API-Driven Merkle Commits](../../docs/adr/0006-gitops-project-declarations-with-api-driven-merkle-commits-and-postgres-projection.md): Git Merkle-tree for tamper-proof auditability, projected to PostgreSQL, mutated via user-friendly API commits.
- [ADR 0007: Scheduler-Dispatcher Split and NATS JetStream Topology](../../docs/adr/0007-scheduler-dispatcher-split-and-nats-topology.md): Dedicated Go Dispatcher for constraint matching and cache affinity; NATS JetStream for durable queues, Core NATS for ephemeral live log streaming.
- [ADR 0008: Database-Managed Project Access Control and Descoped Package Ownership](../../docs/adr/0008-database-managed-acls-and-global-package-ownership.md): Authentik OIDC + JIT user provisioning, PostgreSQL-managed project ACLs, package-level maintainers descoped.
- [01: Research libsolv CGO Integration](issues/01-research-libsolv-cgo.md): Use deterministic greedy expander from `BSSolv.xs` (not standard SAT `solver_solve()`), store binary `.solv` blobs in PostgreSQL loaded via `fmemopen`, and isolate per-worker `Pool`s pinned with `runtime.LockOSThread()`.
- [02: Research obs-build Worker Execution Contract](issues/02-research-obs-build-worker.md): Standardize worker supervisor on `/usr/lib/build/build` CLI contract with `.build.rpmlist` pre-computed from S3, disposable QCOW2 overlays, and standardized exit codes (0/1/2/3/4/9).
- [03: Research S3 RPM Repodata Generation](issues/03-research-s3-repodata.md): Subprocess `createrepo_c` with `--update --recycle-pkglist` on metadata-only S3 syncs, serialized via NATS partitioned subjects and PostgreSQL advisory locks, published atomically via `repomd.xml`.
- [04: Specify Project and Package Manifest Schemas](issues/04-specify-manifest-schemas.md): Canonical YAML schemas for `project.yaml` and sharded `packages/xx/<package>.yaml`, embedding resource sizing, target filtering, and API-driven GitOps mutation.
- [05: Specify NATS JetStream Message Contracts](issues/05-specify-nats-message-contracts.md): Defined Protocol Buffer schemas for triggers, jobs, status, heartbeats, and publication; configured JetStream retention and Core NATS log streaming.
- [06: Prototype Modern CLI and Agent Interface](issues/06-prototype-cli-interface.md): Validated dual-mode architecture (Charm `bubbletea` TUI vs `--json` for agents), command taxonomy, live log streaming, and API-assisted GitOps sharding in working Go prototype.
- [07: Specify Access Control, Maintainers, and Notification Routing](issues/07-specify-access-control-and-maintainers.md): Authentik OIDC integration, JIT provisioning, database-managed project ACLs, and descoped package-level maintainers.

## Frontier Tickets

*(All charted frontier tickets are currently resolved.)*

## Not yet specified

- **Automated Artifact & Repodata Signing**: Integration with Sigstore/Cosign for OCI and per-project GPG keyrings for RPMs.
- **Product Composer Integration**: Workflow pipeline for invoking `product-composer` to generate unified installation media.
- **KubeVirt Autoscaling Engine**: Dynamic provisioning of ephemeral worker VM instances in Kubernetes for x86_64.
- **Worker PXE / Systemd Deployment**: Packaging and configuration strategy for deploying `bsgo-worker` to existing bare-metal fleets.

## Out of scope

- **Legacy WebUI & Frontend**: No Rails, no frontend UI (delegated to modern Git forges and CLI).
- **Legacy XML API / osc Protocol**: Clean break to modern REST/gRPC; no legacy XML backward compatibility layer.
- **Server-Side VCS & Merge Request Engine**: OBS no longer acts as a Git forge or code review tool.
- **Maintenance Incident & QAM Workflows**: Removed in favor of standard Git branch/tag promotion pipelines.
- **Early Build Preemption**: Killing or suspending young running builds to free workers for high-priority jobs is deferred out of scope; only superseded builds are canceled.
- **Built-in Email & Bugzilla Notification Engine**: Notification routing and Bugzilla updates are descoped from core `bsgo` and delegated to external webhook receivers.
- **Package-Level Maintainers & Ownership**: Descoped from the build service; package sources, ownership, and PR review permissions belong strictly to the external Git forge / SCM service. Isolated package rebuilds cannot be triggered without project context.
