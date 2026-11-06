# Issue 01: Research libsolv CGO Integration

Status: closed
Labels: `wayfinder:research`

## Question

How can we cleanly interface Go with openSUSE's `libsolv` C library via CGO to load solvable pools from memory/PostgreSQL, evaluate repository dependency constraints, and calculate minimal build-job dependency sets in sub-millisecond execution times?

## Resolution

Resolved in [docs/research/01-libsolv-cgo.md](../../docs/research/01-libsolv-cgo.md).
- Do not use standard SAT `solver_solve()`: use a deterministic greedy expander derived from `BSSolv.xs` to avoid non-deterministic package selection.
- Store binary `.solv` cache blobs (`BYTEA`) in PostgreSQL and load them in memory using POSIX `fmemopen()` + `repo_add_solv()` (~5ms cold, <0.2ms warm).
- Thread safety: `libsolv` `Pool` is not thread-safe. Pinned goroutine worker pool with `runtime.LockOSThread()` managing per-architecture pools.
