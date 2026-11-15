# Specification: Modern CLI and Agent Interface

This document formalizes the design and contracts for the `bsgo` Command-Line Interface and Agent Integration, validated by the working prototype in `prototype/cli/`.

---

## 1. Dual-Mode Execution Philosophy

The `bsgo` CLI is built on a strict **Dual-Mode Contract**:
1. **Interactive Human Mode (TTY Default)**:
   - Rich terminal aesthetics powered by Charm (`bubbletea` and `lipgloss`).
   - Visual status badges (`● SUCCEEDED`, `◐ BUILDING`, `○ PENDING`, `✖ FAILED`).
   - Interactive live split-pane dashboard (`bsgo watch`) for monitoring jobs, worker fleet capacity, and streaming build logs with keyboard shortcuts.
2. **Machine & Agent Mode (`--json`)**:
   - Deterministic JSON output streams on `stdout`.
   - **Zero ANSI escape sequences**, zero spinners, zero progress bars, and zero interactive prompts.
   - Error details and diagnostics isolated to `stderr`.
   - Optimized for CI pipelines, `jq`, and LLM agents.
3. **Quiet Mode (`-q`, `--quiet`)**:
   - Emits only the primary identifier or status token (e.g. `job-5a6b7c`), enabling clean shell scripting:
     ```bash
     JOB_ID=$(bsgo package trigger openSUSE:Factory/chromium -q)
     ```

---

## 2. Command Taxonomy & Structure

```
bsgo
├── project
│   ├── list                            List all accessible projects
│   ├── show <project>                  Show targets, repositories, and metadata
│   └── convert-obs                     Convert an OBS _meta + prjconf into project.yaml (offline)
├── package
│   ├── list <project>                  List packages within a project
│   ├── show <project/package>          Inspect manifest, git source, and shard path
│   ├── trigger <project/package>       Trigger a build job (optional: --commit <sha>)
│   └── set-resources <project/package> Update worker VM limits (API-assisted Git commit)
├── build
│   ├── status <job-id>                 Inspect job state, worker assignment, and timings
│   ├── logs <job-id> [-f]              Stream live logs via Core NATS or fetch from S3
│   └── cancel <job-id>                 Dispatch abort signal to cancel active build
└── watch                               Open interactive Charm TUI dashboard
```

---

## 3. API-Assisted GitOps Abstraction

A critical discovery of this prototype is how the CLI abstracts away the underlying 2-character hex hash sharding (`packages/xx/<package>.yaml`):

- **The Problem**: Requiring users or agents to manually compute `sha256("chromium")[:2]` = `packages/dd/chromium.yaml`, edit YAML, branch, commit, and push makes basic CLI operations clunky.
- **The Solution**:
  When a user or agent invokes:
  ```bash
  bsgo package set-resources openSUSE:Factory/chromium --memory 64Gi --vcpus 32
  ```
  The CLI calls the control plane API. The API resolves the shard, updates the manifest, creates a signed Git commit, and pushes to the project repository.
  
  **Machine response with `--json`**:
  ```json
  {
    "status": "COMMITTED",
    "project": "openSUSE:Factory",
    "package": "chromium",
    "manifest": "packages/dd/chromium.yaml",
    "git_commit": "a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0",
    "author": "agent@infra.local",
    "new_resources": {
      "memory": "64Gi",
      "vcpus": 32,
      "disk": "100Gi",
      "timeout": "8h"
    }
  }
  ```

### 3.1 Offline OBS Conversion (`bsgo project convert-obs`)

```bash
bsgo project convert-obs --meta <_meta> --prjconf <_config> [--out project.yaml]
                         [--verify-arch <arch>...] [--dry-run] [--force] [--no-verify] [--json]
```

Converts an OBS project (the `_meta` XML and the project configuration) into a `project.yaml` (ADR 0010). It needs no control plane and is non-interactive.

- Conditionals are never flattened. `%if 0%{?_is_in_project}` maps to `protected_config`, `%if 0%{?_is_this_project}` to `private_config`, `%ifarch` to `arch_config.<arch>`, `%if "%_repository" == "<r>"` to `targets.<r>.config`, top-level `%define` to `defines` (ordered), `Macros:` bodies to `macros`. Everything else, and anything the fixed render order would reorder, stays verbatim in `raw_config`.
- Unless `--no-verify` is given, the round-trip gate runs first (every repository, every architecture of `_meta` plus `--verify-arch`, and the consumers: the project, a subproject, a foreign project). On failure the command exits non-zero and writes nothing.
- Output goes to `--out`, or to stdout when `--out` is omitted. `--out` is not overwritten without `--force`; `--dry-run` converts and verifies without writing.
- With `--json`, stdout carries exactly one JSON document and nothing else: `{"out", "dry_run", "verified", "report", "manifest"}` (`manifest` only when `--out` is omitted). `report` has `structured_statements`, `raw_fragments`, `raw_lines`, `order_moves`, `raw_by_reason`, `dropped_comments`, and `raw[]` entries with `scope`, `line`, `lines`, `reason`, `excerpt`. Human summaries go to stderr, never mixed into stdout.

Example:

```bash
bsgo project convert-obs --meta SUSE_SLFO_1.2.meta.xml --prjconf SUSE_SLFO_1.2.prjconf \
  --out project.yaml --json | jq '.report.raw_by_reason'
```

---

## 4. Interactive Live TUI Dashboard (`bsgo watch`)

The live TUI provides real-time visibility into the build cluster:

```
┌────────────────────────────────────────────────────────────────────────────────────────────────┐
│ bsgo  Cloud-Native Build Service Dashboard (NATS JetStream: Connected • Postgres: Synced)      │
├────────────────────────────────────────────────────────┬───────────────────────────────────────┤
│ Active & Pending Jobs                                  │ Worker Fleet Capacity                 │
│                                                        │                                       │
│ ▶ job-9a8b1c  ◐ BUILDING   glibc        x86_64  p:100  │ • worker-x86-01  x86_64   BUSY        │
│   job-7f6e5d  ◐ BUILDING   chromium     x86_64  p:10   │   (32 cores, 128GB) running: glibc    │
│   job-3c2b1a  ● SUCCEEDED  ripgrep      aarch64 p:25   │ • worker-x86-02  x86_64   BUSY        │
│   job-4d5e6f  ○ PENDING    kernel-def   s390x   p:90   │   (16 cores, 64GB)  running: chromium │
│                                                        │ • worker-arm-01  aarch64  IDLE        │
│                                                        │ • worker-s390x-01 s390x   IDLE        │
├────────────────────────────────────────────────────────┴───────────────────────────────────────┤
│ Live NATS Log Stream: bsgo.logs.job-9a8b1c                                                     │
│ [00:00:01] bsgo-worker-x86-01: claiming job from bsgo.workers.worker-x86-01.job               │
│ [00:00:04] obs-build: initializing QEMU/KVM VM overlay root.qcow2 (vCPUs: 16, RAM: 32Gi)       │
│ [00:00:30] obs-build: entering build phase (%build) as user abuild                             │
├────────────────────────────────────────────────────────────────────────────────────────────────┤
│ [↑/↓] Navigate  •  [Enter/l] Toggle Logs  •  [c] Abort Job  •  [q] Quit                        │
└────────────────────────────────────────────────────────────────────────────────────────────────┘
```

---

## 5. Prototype Validation & Artifacts

- **Runnable Code**: `prototype/cli/`
- **Execution Test**:
  ```bash
  cd prototype/cli
  go test -v ./...
  ./bsgo project list
  ./bsgo package show openSUSE:Factory/chromium
  ./bsgo build status job-9a8b1c --json
  ```
