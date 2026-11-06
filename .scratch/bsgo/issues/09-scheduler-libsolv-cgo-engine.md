# 09: Scheduler: libsolv CGO Deterministic Solver Engine

**What to build:**
Implement the in-process dependency solver engine in Go binding openSUSE's `libsolv` C library via CGO. The engine loads pre-compiled binary `.solv` cache blobs (`BYTEA`) directly from PostgreSQL memory into C memory using POSIX `fmemopen()` and `repo_add_solv()`. It executes a deterministic greedy expander (derived from `BSSolv.xs`), enforcing project `prefer`/`ignore` rules, Tarjan SCC dependency cycle sorting, and halting with explicit typed errors (`ErrChoice`, `ErrNoProvider`) if ambiguity exists. Isolated `Pool` instances are managed inside a worker pool pinned with `runtime.LockOSThread()`.

**Blocked by:** 08: Foundation: Project and Package Ingestion Gateway with CLI

**Status:** ready-for-agent

- [ ] CGO package `pkg/solv` wrapping `libsolv` C headers with clean Go structs (`Pool`, `Repo`, `Expander`).
- [ ] Memory loader streaming byte slices into `libsolv` repos via POSIX `fmemopen()`.
- [ ] Deterministic greedy expander implementation preventing non-deterministic SAT backtracking.
- [ ] Tarjan Strongly Connected Components (SCC) topological sorter for chroot package sequencing.
- [ ] Thread-pinned solver worker pool using `runtime.LockOSThread()` managing isolated per-architecture pools.
- [ ] Table-driven unit tests verifying dependency resolution, conflict handling, ambiguity errors, and cycle detection against distribution testdata in <1ms execution times.
