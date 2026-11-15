# Specification: Modern CLI and Agent Interface

This document formalizes the design and contracts for the `bsgo` Command-Line Interface and Agent Integration, validated by the working prototype in `prototype/cli/`.

---

## 1. Dual-Mode Execution Philosophy

The `bsgo` CLI is built on a strict **Dual-Mode Contract**:
1. **Interactive Human Mode (TTY Default)**:
   - Rich terminal aesthetics powered by Charm (`bubbletea` and `lipgloss`).
   - Visual status badges (`● SUCCEEDED`, `◐ BUILDING`, `○ PENDING`, `✖ FAILED`).
   - Live dashboard (`bsgo watch <project>`) of the persisted build state with `--once`, `--json` and `--history` for agents.
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
│   ├── convert-obs                     Convert an OBS _meta + prjconf into project.yaml (offline)
│   ├── import-obs-git                  Import an OBS project kept in a git superproject (offline)
│   ├── apply <dir>                     Upsert project.yaml and all package manifests of a directory
│   └── dependency remove <project>     Remove a named dependency (e.g. the Bootstrap Dependency)
├── package
│   ├── list <project>                  List packages within a project
│   ├── show <project/package>          Inspect manifest, git source, and shard path
│   ├── trigger <project/package>       Trigger a build job (optional: --commit <sha>)
│   └── set-resources <project/package> Update worker VM limits (API-assisted Git commit)
├── build
│   ├── status <job-id>                 Inspect job state, worker assignment, and timings
│   ├── logs <job-id> [-f]              Stream live logs via Core NATS or fetch from S3
│   └── cancel <job-id>                 Dispatch abort signal to cancel active build
└── watch <project>                     Live build-state TUI (--once, --history, --json)
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

### 3.2 Offline Import from a Git Superproject (`bsgo project import-obs-git`)

```bash
bsgo project import-obs-git --repo <git dir> --ref <branch|commit> --out <dir> [--meta <_meta>]
        [--prjconf-path _config] [--gitea-base <url>] [--default-branch <b>]
        [--bootstrap-url <url> --bootstrap-name <name> --bootstrap-targets <t>...]
        [--verify-arch <arch>...] [--dry-run] [--force] [--no-verify] [--json]
```

Reads a superproject such as `products/SLFO` with git plumbing only (`rev-parse`, `ls-tree`, `show`; submodule directories may be empty) and writes `<out>/project.yaml` (same code path and round-trip gate as `convert-obs`) and one `<out>/packages/xx/<pkg>.yaml` per mode-160000 tree entry (`xx` per ADR 0001).

- Package manifests hold the git URL from `.gitmodules` (relative URLs resolve against `--gitea-base`, else the `origin` remote of `--repo`, with git semantics), the `.gitmodules` branch (or `--default-branch`, else unset), and the full pinned commit id. They contain no `recipe` (auto-detected) and no `resources` (nothing in the source defines them); `build.enabled` is true. Per-package exclusions stay in the project's `build_flags` (`excludebuild:`).
- `--bootstrap-url`, `--bootstrap-name` and `--bootstrap-targets` append a `url` dependency with that `name` after the project's own path entries on each listed target (Bootstrap Dependency). There is no default target.
- Packages in `.gitmodules` but not in the tree, or the reverse, are skipped with a diagnostic on stderr and listed in the JSON report (`packages.missing_in_tree`, `packages.missing_in_gitmodules`, `packages.skipped`).
- `--json` prints one document `{"out","dry_run","project","verified","report","packages"}`; `project.yaml` is not overwritten without `--force`.

### 3.3 Applying a Project Directory (`bsgo project apply`)

```bash
bsgo project apply <dir> [--dry-run] [--json]
```

Upserts `<dir>/project.yaml` and every `packages/**/*.yaml`. Each package must sit at its shard path. All files are tried; failures are listed (`failures[]` with `file` and `error`) and the exit code is non-zero if any occurred. `--dry-run` only validates.

### 3.4 Removing a Dependency (`bsgo project dependency remove`)

```bash
bsgo project dependency remove <project> --target <target> --name <name> [--yes] [--dry-run] [--json]
```

Removes the dependency with that `name` from one target of the stored project (used to drop the Bootstrap Dependency). Requires `--yes` unless `--dry-run`. Fails if the project, target or name does not exist.

---

## 4. Watching a Project (`bsgo watch`)

```bash
bsgo watch <project> [--repo R] [--arch A] [--state S] [--interval 2s]
bsgo watch <project> --once [--state S] [--json]
bsgo watch <project> --history <package> [--limit 20] [--json]
```

The project is required (positional, or `-p/--project`). A missing or unknown project fails with the list of known projects.

**Data.** The current state is read from the PostgreSQL table `package_build_state` (the same source and counts as `bsgo project status`), polled every `--interval`. One row is one package unit (`package[:flavor]`) in one repo and arch with its current or last job. NATS is only used for live build logs (`bsgo.logs.<jobid>`), worker names (heartbeats on `bsgo.workers.heartbeat` carry `active_job_ids`) and to refresh immediately when a `bsgo.status` message arrives. The TUI keeps working, without logs and worker names, if NATS is unreachable.

**Default view.** Packages in state `building`: package[:flavor], repo/arch, elapsed time (`building_seconds`), short job id and worker, longest-building first. The header shows the counts per state.

| Key | Action |
|-----|--------|
| `b` `s` `f` `u` `k` | building (default), scheduled, failed, unresolvable, blocked |
| `a` | all states |
| `/` | search by package substring (Enter keeps, Esc clears) |
| Up/Down | select a row |
| Enter, `l` | logs of the selected job (saved log plus live stream; Esc closes) |
| `m` | architecture matrix (counts per arch and state; not the default) |
| `q` | quit |

**`--once`** prints the snapshot of `--state` (default `building`, `all` for every state) as a plain table and exits. With `--json` it emits one deterministic document on stdout, sorted by package, flavor, repo, arch, with no ANSI:

```json
{"project": "...", "repo": "...", "arch": "...", "state": "building",
 "counts": {"building": 3, "failed": 1},
 "packages": [{"package": "glibc", "repo": "standard", "arch": "x86_64", "state": "building",
               "job_id": "...", "updated_at": "...", "building_seconds": 2832}]}
```

`packages[]` uses the entry type of `bsgo project status --json`. Worker names are only resolved in the TUI.

**`--history <package>`** lists earlier jobs of one package, newest first, one entry per job with its latest status (`job_id`, `phase`, `repo`, `arch`, `worker`, `exit_code`, `reason`, `last_update_at`). It reads the `JOB_STATUS` stream backwards, at most 20000 messages, and stops after `--limit` jobs (default 20). `--repo` and `--arch` narrow it.

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
