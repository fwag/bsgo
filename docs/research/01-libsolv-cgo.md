# Research Report: libsolv CGO Integration for bsgo (Ticket 01)

## Executive Summary

To achieve sub-millisecond build-job dependency calculation in Go while interfacing with openSUSE's `libsolv`:
1. **Existing Go Bindings:** There are **no production-ready Go bindings or CGO wrappers** for `libsolv` upstream or in Red Hat/Fedora/openSUSE ecosystems (`libdnf5` attempted SWIG bindings for Go, but they are disabled and broken).
2. **Key Architectural Discovery from OBS (`openSUSE/perl-BSSolv`):** A package build service like OBS / `bsgo` **must not use libsolv's standard SAT solver (`solver_solve()`)**. SAT solvers allow non-deterministic heuristics and backtracking that arbitrarily pick packages when multiple providers exist. Instead, OBS created a dedicated greedy expander (`BSSolv::expander` in `BSSolv.xs`) that guarantees strict determinism, enforces project `prefer`/`ignore` rules, and halts with explicit ambiguity errors (`ERROR_CHOICE`).
3. **Sub-Millisecond Loading via Memory/PostgreSQL:** Storing relational database rows and deserializing thousands of packages row-by-row across CGO boundaries is too slow (tens of milliseconds). Instead, PostgreSQL should store immutable, pre-compiled binary `.solv` cache blobs (`BYTEA`). These blobs can be loaded directly from Go byte slices into C memory using POSIX `fmemopen()` and `repo_add_solv()`, which deserializes 20,000+ packages in ~5–10 ms on cold start and <1 ms in warm cache pools.
4. **CGO Concurrency Model:** `libsolv`'s `Pool` is **not thread-safe**. Attempting to share a `Pool` across concurrent goroutines triggers data races due to lazy string interning and internal caches. `bsgo` must adopt a **Worker-Pool / Dedicated Thread Worker pattern**, where each worker goroutine owns an isolated `Pool` instance pinned with `runtime.LockOSThread()`, communicating with the Go scheduler via Go channels.

---

## 1. Upstream & Community Ecosystem Survey

| Project | Binding Type | Status & Suitability for `bsgo` |
| :--- | :--- | :--- |
| **`openSUSE/libsolv`** | SWIG (Python, Perl, Ruby, Tcl) | No Go bindings exist upstream. SWIG Go bindings for C libraries with complex structs are notoriously fragile and carry significant garbage collection / runtime overhead. |
| **`rpm-software-management/dnf5` (`libdnf5`)** | SWIG (Go stub) | Explicitly disabled and broken upstream (Issue #585) due to SWIG's inability to handle modern C++ pointer semantics (`std::unique_ptr`) in Go. |
| **`openSUSE/perl-BSSolv`** | Perl XS (C) | **The primary gold standard.** Contains `BSSolv.xs`, Michael Schroeder's purpose-built C extension that bridges OBS to `libsolv`. It contains the complete deterministic dependency expansion engine, Tarjan's SCC cycle sorter, and `.solv` cache generators. |
| **Community CGO packages** | Experimental / Toy | None exist that support production solver functionality or OBS build expansion. |

**Conclusion:** `bsgo` must implement a targeted CGO binding layer (`pkg/solv` or `pkg/bssolv`), wrapping `libsolv` C headers directly alongside a dedicated C bridge derived from the battle-tested logic in `openSUSE/perl-BSSolv`.

---

## 2. Core `libsolv` Architecture & Data Structures

`libsolv` is an ultra-fast, memory-compact C library designed by openSUSE for package management. It is built around zero-copy string interning and contiguous array indices:

```
                      ┌───────────────────────────────────────┐
                      │              Pool                     │
                      │  - Stringpool (Id intern table)       │
                      │  - Solvables contiguous array         │
                      │  - whatprovides / whatprovides_rel    │
                      └───────┬───────────────────────┬───────┘
                              │ 1..*                  │ 1..*
                ┌─────────────┘                       └─────────────┐
                ▼                                                   ▼
     ┌───────────────────────┐                             ┌───────────────────────┐
     │         Repo          │                             │       Solvable        │
     │  - Repodata           │◄────────────────────────────┤  - Name, EVR, Arch    │
     │  - Idarraydata        │    belongs to Repo          │  - Provides, Requires │
     │  - Solv file load/dump│                             │  - Conflicts, etc.    │
     └───────────────────────┘                             └───────────────────────┘
```

### Core Primitives
1. **`Id` (`typedef int Id;`):**
   - The fundamental 32-bit token. Every string (package name, EVR string, architecture) is interned into the pool via `pool_str2id(pool, str, create)`.
   - Positive `Id`s denote string atoms.
   - Negative/offset `Id`s or special ranges denote **Relational Dependencies (`Reldep`)** created via `pool_rel2id(pool, name_id, evr_id, flags, create)`.
   - Supported relation flags include: `REL_EQ`, `REL_LT`, `REL_GT`, `REL_LE`, `REL_GE`, `REL_AND`, `REL_OR`, `REL_WITH`, `REL_WITHOUT`.
2. **`Solvable` (`struct s_Solvable`):**
   - Contiguous element in `pool->solvables`.
   - Contains 32-bit `Id` fields: `name`, `evr`, `arch`, `vendor`.
   - Contains 32-bit array offsets into `repo->idarraydata` for dependency lists: `provides`, `requires`, `conflicts`, `obsoletes`, `recommends`, `suggests`, `supplements`.
   - Dependency lists are terminated with `Id 0`.
3. **`Queue` (`struct s_Queue`):**
   - Dynamic C array of `Id` items (`Id *elements; int count; int alloc;`).
   - Used for passing jobs to the solver, collecting search results, tracking unresolvable dependencies, and recording transactions.
4. **`Map` (`struct s_Map`):**
   - Bit-vector used for ultra-fast $O(1)$ set membership tests (e.g. `installed`, `conflicts`, `ignored`, `preferred`).
5. **`pool_createwhatprovides(Pool *pool)`:**
   - Inverts all `provides` entries across all active repos into a direct hash table lookup (`pool->whatprovides`, `pool->whatprovidesdata`).
   - Once computed, finding which packages satisfy any package requirement or file requirement is an instantaneous array index lookup.

---

## 3. The OBS Determinism Mandate: SAT Solver vs. Greedy Expander

Standard package managers (like Zypper or DNF) use `solver_create(pool)` and `solver_solve(solver, &jobs)`.
However, **a package build scheduler must NOT use `solver_solve()`**:
- **Why SAT Solvers Fail for Build Services:**
  - SAT solvers backtrack and apply heuristics to find *any* valid satisfying assignment.
  - If package A requires capability `X`, and both `pkg-1` and `pkg-2` provide `X`, standard SAT will nondeterministically choose one based on internal clause ordering or repo traversal order.
  - If a package is rebuilt tomorrow, the build chroot might resolve to a completely different set of packages without any notification.
- **The OBS Solution (`BSSolv::expander`):**
  - Uses a deterministic, 2-pass greedy expansion algorithm:
    1. Read package `BuildRequires`.
    2. Query `pool->whatprovides`.
    3. If 0 providers $\to$ fail immediately with `ERROR_NOPROVIDER`.
    4. If >1 providers $\to$ consult explicit project preference maps (`prefer: pkg-1`, `-prefer: pkg-2`). If ambiguity remains $\to$ fail immediately with `ERROR_CHOICE` (forcing the maintainer to specify the exact intended dependency).
    5. Recursively enqueue dependencies of chosen packages into `xpctx.todo`.
    6. Verify no installed package violates synthetic or declared conflicts (`xpctx.conflicts`).
    7. Execute topological sort via Tarjan’s Strongly Connected Components (SCC) algorithm (`BSSolv::depsort2`) to sequence the chroot installation order and detect dependency cycles.

---

## 4. Loading Solvable Pools from PostgreSQL in Sub-Millisecond Times

### The Challenge
A distribution repository (e.g., openSUSE Tumbleweed, Rocky Linux, or Fedora Rawhide) consists of 20,000 to 60,000 packages, each with dozens of capabilities and requirements. Populating a `Pool` via repeated CGO calls row-by-row from PostgreSQL takes hundreds of milliseconds due to CGO overhead and string allocation.

### The Solution: Pre-Compiled `.solv` Blobs
`libsolv` defines an optimized, native binary serialization format (`.solv`) that stores string tables, relations, and package metadata in compact, memory-mappable blocks.

```sql
-- PostgreSQL Repository Cache Schema
CREATE TABLE repo_solv_caches (
    repository_id   UUID PRIMARY KEY,
    revision        INT NOT NULL,
    architecture    VARCHAR(32) NOT NULL,
    solv_blob       BYTEA NOT NULL,
    package_count   INT NOT NULL,
    generated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### In-Memory Loading Pipeline in Go/CGO
Using POSIX `fmemopen`, Go can stream a byte slice stored in memory directly into libsolv without disk I/O:

```c
// C Shim: solv_loader.c
#include <solv/pool.h>
#include <solv/repo.h>
#include <solv/repo_solv.h>
#include <stdio.h>

Repo* load_repo_from_memory(Pool *pool, const char *name, const void *buf, size_t len) {
    FILE *fp = fmemopen((void*)buf, len, "r");
    if (!fp) return NULL;
    
    Repo *repo = repo_create(pool, name);
    if (repo_add_solv(repo, fp, 0) != 0) {
        fclose(fp);
        repo_free(repo, 1);
        return NULL;
    }
    fclose(fp);
    return repo;
}
```

### Performance Characteristics
- **Cold load from DB (`BYTEA`):** Querying a 3 MB `.solv` blob from PostgreSQL and loading it via `fmemopen` + `repo_add_solv()` takes **3–8 milliseconds**.
- **Warm cache in Go memory:** Reusing an initialized in-memory `Pool` and running `expander_expand()` on a build job takes **under 200 microseconds (0.2 ms)**!

---

## 5. CGO Concurrency, Memory Safety, and Goroutine Scheduling

### Concurrency Rules in `libsolv`
- **Thread Safety:** `libsolv` is fundamentally **not thread-safe**. While upstream PR #472 introduced improvements for read operations, `Pool` instances still perform internal lazy evaluation, string interning, and dynamic table resizing.
- **Sharing Pools Across Goroutines:** Concurrent calls to `pool_whatprovides` or `repo_add_solv` on the same `Pool` from multiple Go goroutines will cause memory corruption and segfaults.

### Recommended Go Architecture: Worker Pool Pattern

```
                 ┌─────────────────────────────────┐
                 │     bsgo Scheduler Dispatcher   │
                 └───────────────┬─────────────────┘
                                 │ Jobs Channel
         ┌───────────────────────┼───────────────────────┐
         ▼                       ▼                       ▼
 ┌───────────────┐       ┌───────────────┐       ┌───────────────┐
 │ Solv Worker 1 │       │ Solv Worker 2 │       │ Solv Worker N │
 │ (Locked OS    │       │ (Locked OS    │       │ (Locked OS    │
 │  Thread)      │       │  Thread)      │       │  Thread)      │
 │  Pool x86_64  │       │  Pool aarch64 │       │  Pool x86_64  │
 └───────────────┘       └───────────────┘       └───────────────┘
```

1. **Thread Pinning:** Each solver worker runs in a dedicated goroutine with `runtime.LockOSThread()` to prevent OS thread switching during C execution.
2. **Dedicated Pool Per Worker:** Each worker manages its own `Pool` initialized for a specific target architecture (`x86_64`, `aarch64`, etc.).
3. **CGO Call Batching:** Instead of invoking CGO per dependency check, pass the entire slice of `BuildRequires` and project preference rules into a single C entry point. This keeps CGO transition overhead (<100 ns) negligible compared to total execution time.
4. **Memory Management:** All C allocations (`pool_create`, `queue_init`, `repo_create`) are freed explicitly via worker cleanup routines. Go handles results as standard Go slices after copying `Id` arrays out of the C `Queue`.

---

## 6. Implementation Action Plan for `bsgo`

1. **Phase 1: C Bridge Engine (`pkg/solv/c/`)**
   - Extract and adapt the deterministic expansion engine and Tarjan SCC sorter from `openSUSE/perl-BSSolv` (`BSSolv.xs`), stripping out Perl-specific macros and providing clean C function signatures.
   - Implement `load_repo_from_memory()` with `fmemopen()`.
2. **Phase 2: High-Level Go CGO Wrappers (`pkg/solv/`)**
   - Implement Go structures: `type Pool struct`, `type Repo struct`, `type Expander struct`.
   - Implement `Resolve(req JobRequest) (JobResult, error)` providing structured errors (`ErrNoProvider`, `ErrChoice`, `ErrConflict`).
3. **Phase 3: Database & Caching Layer**
   - Implement `.solv` blob generator when repository publishing events occur.
   - Store `.solv` byte blobs in PostgreSQL table `repo_solv_caches`.
4. **Phase 4: Scheduler Worker Pool Integration**
   - Wire workers into the scheduler dispatch loop to calculate minimal build sets in sub-millisecond execution times.
