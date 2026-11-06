# bsgo - Build Service Go

[![Go Report Card](https://goreportcard.com/badge/github.com/openSUSE/bsgo)](https://goreportcard.com/report/github.com/openSUSE/bsgo)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

**`bsgo`** is a clean-slate, cloud-native reimplementation of the **Open Build Service (OBS)** backend in Go, engineered from the ground up for Kubernetes, S3-compatible object storage, NATS JetStream event streaming, and AI software factories.

It replaces twenty years of legacy Perl daemons, proprietary CPIO version control, and shared POSIX/NFS storage bottlenecks with an ultra-fast, horizontally scalable, and secure distribution build engine.

---

## 🚀 Key Advantages & Project Pros

### 1. Deterministic Greedy Dependency Resolution (In-Process `libsolv`)
- **No Non-Deterministic SAT Backtracking**: Standard SAT solvers (`solver_solve`) apply heuristic backtracking that arbitrarily picks between alternative providers, causing silent non-reproducible build chroots. `bsgo` implements a **deterministic greedy expander** (derived from `BSSolv.xs`) that guarantees identical package selection across rebuilds.
- **Explicit Ambiguity Halting**: Halts immediately with typed errors (`ErrChoice`, `ErrNoProvider`) when multiple unselected packages satisfy a capability without an explicit project `prefer` rule, forcing packagers to resolve ambiguity intentionally.
- **Tarjan Topological Sequencing**: Analyzes dependency graphs to strictly sequence installation orders so base prerequisites (e.g. `glibc`) are installed before consumers (e.g. `gcc`).
- **Sub-Millisecond In-Memory Caching**: Loads pre-compiled binary `.solv` blobs directly from PostgreSQL memory via POSIX `fmemopen()` and thread-pinned worker pools (`runtime.LockOSThread()`), resolving complex dependency trees in **under 1 millisecond**.

### 2. Pure S3 Object Storage (Zero POSIX/NFS Bottlenecks)
- **No `ReadWriteMany` Shared Storage**: Completely removes shared NFS/CIFS filesystems.
- **S3-Compatible Everywhere**: Stores all compiled RPMs, container layers, build logs, and published repository metadata in S3 (compatible with Garage S3, Ceph RGW, and AWS S3).
- **Stateless Control Plane**: Control plane pods in Kubernetes are 100% stateless, enabling effortless horizontal scaling and cluster autoscaling.

### 3. Git-Native Source Control & 256 Consistent Hash Shards
- **Standard Git Forges**: Uses standard Git repositories (Gitea, GitHub, GitLab) as the source of truth, leveraging Git's Merkle tree for cryptographic auditability and pull request code review.
- **Git LFS for Large Binary Archives**: Keeps Git history lightweight while seamlessly handling gigabyte source tarballs stored in S3.
- **Consistent Hash Sharding**: Distributes package manifests across 256 subdirectories (`packages/<hash2>/<package>.yaml` where `<hash2>` is `sha256(pkg)[:2]`), preventing Git directory tree bloat, filesystem inode limits, and merge conflicts when scaling past 16,000 packages.
- **API-Assisted GitOps Mutations**: Users and AI agents modify package resources via CLI commands (`bsgo package set-resources`), while the API resolves the hash shard and makes signed Git commits automatically.

### 4. Scheduler-Dispatcher Decoupling (High Throughput & Low Latency)
- **Critical-Path Prioritization**: The Go Dispatcher analyzes dependency graphs and prioritizes packages by topological dependency depth, ensuring foundational blockers (`glibc`, `gcc`) compile first rather than starving behind leaf packages.
- **Warm-Cache Worker Affinity**: Scores workers based on which local SSD caches already house prerequisite RPMs, dropping chroot bootstrap times from minutes to seconds.
- **Targeted Cancellation**: Automatically aborts running builds on workers the moment a newer Git commit is pushed for that package, immediately reclaiming capacity.

### 5. Multi-Architecture Worker Isolation (QEMU/KVM Overlays)
- **Bare-Metal & PXE Native**: Standalone Go daemons (`bsgo-worker`) run natively on existing bare-metal hardware (x86_64, aarch64, s390x, ppc64le) without forcing Kubernetes onto incompatible mainframes.
- **Disposable QCOW2 Overlays**: Boots sterile QEMU/KVM virtual machines with disposable copy-on-write disk overlays on top of pristine base images, ensuring 100% isolation between untrusted package compilation jobs.
- **Zero-Overhead Ephemeral Log Streaming**: Workers stream live stdout/stderr logs over ephemeral Core NATS pub/sub (`bsgo.logs.<job_id>`), consuming zero disk space when unmonitored.

### 6. AI Software Factory & Agent Ergonomics
- **Dual-Mode CLI Contract**: Interactive human view powered by Charm `bubbletea` and `lipgloss`, paired with deterministic, unadorned JSON on `stdout` (`--json`) and isolated diagnostics on `stderr` for autonomous AI agents and CI pipelines.
- **Self-Documenting Introspection**: Rich, unambiguous CLI `--help` with concrete examples and non-interactive `--yes`/`-y` mutation flags.
- **Fine-Grained Token ACLs**: Resource-scoped Personal Access Tokens (`<action>:<resource>[:<id>]`) strictly contain the blast radius of autonomous AI agents (e.g. `project:write:home:ai-agent`).

### 7. Zero-Download Incremental Repodata Publishing
- **Metadata-Only S3 Sync**: `bsgo-publisher` downloads only existing `repodata/` XML files (~5–20MB) and newly compiled RPMs, executing `createrepo_c --update --recycle-pkglist --no-database` without downloading historical binaries.
- **Atomic "Commit-by-Pointer"**: Uploads new hash-prefixed XML files first, followed by an atomic overwrite of `repomd.xml` on S3, ensuring package managers (`zypper`, `dnf`) never observe inconsistent repository states.

---

## 🏛️ Architecture Overview

```
                               ┌─────────────────────────┐
                               │   Git Forge (Gitea/GH)  │
                               └────────────┬────────────┘
                                            │ Webhook Trigger
                                            ▼
┌────────────────────────────────────────────────────────────────────────┐
│                        bsgo Control Plane (K8s)                        │
│                                                                        │
│   ┌──────────────────────────┐        ┌────────────────────────────┐   │
│   │         bsgo-api         │        │       bsgo-scheduler       │   │
│   │  - Authentik OIDC / PAT  │───────►│  - In-process libsolv CGO  │   │
│   │  - Manifest Ingestion    │        │  - Deterministic Expander  │   │
│   └─────────────┬────────────┘        └─────────────┬──────────────┘   │
│                 │                                   │                  │
│                 │ Postgres Projection               │ Emits BuildJob   │
│                 ▼                                   ▼                  │
│   ┌──────────────────────────┐        ┌────────────────────────────┐   │
│   │        PostgreSQL        │        │       NATS JetStream       │   │
│   │  - Projects & Packages   │        │  - JOBS_PENDING (WorkQueue)│   │
│   │  - Binary .solv Blobs    │        │  - TRIGGERS & PUBLISH      │   │
│   │  - Project ACLs          │        └─────────────┬──────────────┘   │
│   └──────────────────────────┘                      │                  │
│                                                     ▼                  │
│                                       ┌────────────────────────────┐   │
│                                       │       bsgo-dispatcher      │   │
│                                       │  - Critical-Path Priority  │   │
│                                       │  - Warm-Cache Affinity     │   │
│                                       └─────────────┬──────────────┘   │
└─────────────────────────────────────────────────────┼──────────────────┘
                                                      │
                       Point-to-Point Dispatch        │ (bsgo.workers.<id>.job)
                       ───────────────────────────────┘
                                      │
            ┌─────────────────────────┼─────────────────────────┐
            ▼                         ▼                         ▼
   ┌─────────────────┐       ┌─────────────────┐       ┌─────────────────┐
   │   bsgo-worker   │       │   bsgo-worker   │       │   bsgo-worker   │
   │  (x86_64 Host)  │       │ (aarch64 Host)  │       │  (s390x Host)   │
   │ - QEMU/KVM VM   │       │ - QEMU/KVM VM   │       │ - QEMU/KVM VM   │
   │ - QCOW2 Overlay │       │ - QCOW2 Overlay │       │ - QCOW2 Overlay │
   └────────┬────────┘       └────────┬────────┘       └────────┬────────┘
            │                         │                         │
            └─────────────────────────┼─────────────────────────┘
                                      ▼
                        ┌──────────────────────────┐
                        │    Garage S3 Storage     │
                        │  - RPM Binaries          │
                        │  - repodata/ (repomd.xml)│
                        │  - Full Build Logs       │
                        └──────────────────────────┘
```

---

## 🛠️ Quick Start & Developer Guide

### Prerequisites
- **Go**: 1.22+
- **libsolv development headers**:
  ```bash
  sudo zypper in libsolv-devel createrepo_c
  ```

### Build & Run
```bash
# Clone the repository
git clone https://github.com/openSUSE/bsgo.git && cd bsgo

# Run test suites (executes in <0.1s)
go test -v ./...

# Run strict reviewability linter (evergit standard)
golangci-lint run --config .golangci.yml ./...

# Compile bsgo CLI
go build -o bsgo ./cmd/bsgo

# Explore CLI commands
./bsgo --help
./bsgo project list
./bsgo project list --json
```

---

## 📋 Documentation & Specifications

- **[Architecture Decision Records (ADRs)](docs/adr/)**: Architectural pillars 0001 through 0008.
- **[Research Reports](docs/research/)**: Deep investigations into `libsolv` CGO, `obs-build` contracts, and S3 repodata.
- **[Specifications](docs/specs/)**:
  - `01-manifest-schemas.md`: YAML schemas for `project.yaml` and sharded `packages/xx/<package>.yaml`.
  - `02-nats-message-contracts.md`: Protocol Buffer message contracts and stream topologies.
  - `03-cli-and-agent-interface.md`: Dual-mode CLI and interactive Charm Bubble Tea dashboard.
  - `04-access-control-and-auth.md`: Authentik OIDC, JIT user provisioning, and scoped agent tokens.
- **[AI Software Factory Guardrails](AGENTS.md)**: Engineering mandates and quality rules for human and AI agents.
