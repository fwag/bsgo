# Plan: Build SUSE:SLFO:1.2 (x86_64) bootstrapped from SUSE:SLFO:1.1

Status: proposed (2026-11-15)
Audience: implementation agents (smaller models) + human reviewer
Scope: `standard` repository, `x86_64` only, no debuginfo/debugsource. Other
arches/repos must keep working *structurally* (no new hardcoding), but are not
validated here.

---

## 0. How to use this document

- Work packages (WP) are ordered. Each WP lists: goal, files, tasks, acceptance
  criteria, tests. Do not start a WP before its `Depends on` WPs are merged.
- Every WP must pass the AGENTS.md gates (`golangci-lint run`, `gosec ./...`,
  `govulncheck ./...`, table-driven tests, funlen/nestif/gocognit/dupl limits).
  Podman only, never `docker`.
- **Never hardcode** `"standard"`, `"x86_64"`, project names, or repo URLs.
  Values come from the job / project manifest. If a value is missing, return
  an error; do not default silently (see §1.9 for existing offenders).
- The OBS reference implementation lives in `~/Devel/open-build-service/src/backend`
  and obs-build in `/usr/lib/build` (package `build`, installed on both the dev
  box and the test box). Function references below are `file:sub`.
- The **oracle** for any dependency/config/spec logic is obs-build's Perl code
  (`/usr/lib/build/Build.pm`, `Build/Rpm.pm`, `Build/Expand.pm`,
  `/usr/lib/build/expanddeps`). Parity tests compare bsgo output to the oracle;
  they are the definition of "correct" for WP1–WP3.

---

## 1. Findings (evidence from the live test on 172.16.20.68, 2026-11-15)

Status shown by `bsgo project status SUSE:SLFO:1.2`: 4 succeeded, 1881
unresolvable, 1600 pending, 17 failed. Reality in `worker.log`: 775 builds
started, 461 distinct packages, 544 `build SUCCEEDED`, 183 `FAILED`, 37
`ABORTED`. The system is not stuck; it is not converging, and it misreports.

### 1.1 The scheduler never parses the spec (root cause of "unresolvable")
`pkg/scheduler/scheduler.go:buildJobForArch` uses
`buildReqs := []string{pkg.Package.Name}` when `pkg.Build.Requires` is empty,
which it is for all 3503 manifests. It resolves the *source* package name as if
it were a *binary* package. Server log: 6074 failures, e.g.
`no provider found for requirement: Botan` (source name, no such binary) and
`multiple conflicting providers ... libcurl.so.4()(64bit)` (runtime deps of a
same-named binary from 1.1). `pkg/scheduler/cascade.go:checkPackageCascade`
does the same thing.
OBS: the source server runs `Build::parse` on the recipe with the repo's
config (`/usr/lib/build/Build.pm:parse`, `Build/Rpm.pm:parse`) and gives the
scheduler `dep`, `subpacks`, `name`, `exclarch`, and so on.

### 1.2 The homegrown expander is not OBS-compatible
`pkg/solv/c_bridge.c:bsgo_expand` / `select_provider`:
- `pool_str2id(reqs[i])` turns `foo >= 1.2`, `(a or b)` and `pkgconfig(x) = 1`
  into literal names. Versioned and rich deps can never match.
- Ignores `Substitute`, `Conflict`, negative `Prefer: -pkg`, context prefers
  (`Prefer: a:b`), `ExpandFlags` (e.g. `keepfilerequires`), and repo priority.
  `Ignore: a:b` is applied globally instead of only for `a`.
- Has an invented "glibc/bash/coreutils" heuristic, and the scheduler appends
  invented prefers (`filesystem glibc bash ...`).
- No path layering. In OBS the first repo in the path that has a package name
  hides all same-named packages in later repos.
- A new pool is built, and the full primary.xml re-decompressed and re-parsed,
  on **every** resolve (`worker_pool.go:runWorker`, `solv.go:loadRPMRepos`).
OBS reference: `/usr/lib/build/Build/Expand.pm` (the pure-Perl expander, used
when BSSolv is absent) and `BSSched/Checker.pm:createpool/preparepool`.

### 1.3 Binaries built in-project are indexed without provides/requires
`pkg/publisher/publisher.go:updateTargetSolvCache` →
`solv.ParseRPMFilename` → `MergeAndDumpSolv`: each RPM gets only `name` and
`name = evr`. Its `libfoo.so.1()(64bit)`, `pkgconfig(...)` and `cmake(...)`
provides are lost. Each publish also re-appends every RPM to the existing
blob, so duplicates grow without bound.
OBS: `BSSched/BuildRepo.pm` (`:full` tree, `fctx_integrate_package_into_full`,
`writesolv`) keeps real header data. A package's new binary set *replaces*
its old set.

### 1.4 There is no OBS-style scheduler loop
bsgo runs trigger → resolve → dispatch, with an in-memory `MemoryGatekeeper`
keyed by `project:package` (no repo or arch). Missing pieces:
- **Dependency ordering / blocking.** Packages are dispatched before their
  in-project providers exist. Example failures in job logs:
  `nothing provides python313-pip` (policycoreutils),
  `nothing provides pkgconfig(leancrypto)` (gnutls),
  `nothing provides cmake(Qt6Core) = 6.9.1` (qt6-scxml). OBS would mark these
  `blocked` or `unresolvable` and build the providers first.
- **meta-based rebuild decision.** OBS rebuilds only when the hash of
  (source + all build-dep hdrmd5s) changes (`BSSched/BuildJob.pm:metacheck`,
  `BuildJob/Package.pm:check`). bsgo's cascade matches by
  `strings.HasPrefix(p.Name, eventPkg)` (`cascade.go:checkDependencyMatch`) and
  retriggers everything that looks related. 7zip was built 12 times.
- **Cycle handling** (`Checker.pm:handlecycle/cycsort`, `BuildFlags: genmetaalgo:1`).
- **Persistent per-(project, repo, arch, package) state** with reason text.
  Today state is the last message in the `bsgo.status` stream, so a cascade's
  `PENDING` overwrites `SUCCEEDED`.

### 1.5 prjconf → project.yaml conversion is lossy (the model is fine, see D1)
SLFO `_config` has 38 `%if`/`%ifarch` blocks, `%_repository` conditionals,
`%define` lines used by directives (e.g. `Prefer: %{lua_pref} ...`), a
`Release:` line, `Runscripts`, `Hostarch` and `Patterntype`. The converter
flattened all branches (e.g. duplicate `%_with_gccgo_go121`), dropped
`Release:`, and moved `%define`s into `Macros:`. The worker does not use
`_config` at all. It renders a new config from YAML
(`pkg/manifest/config_render.go`), and that config contains literal
`Prefer: %{lua_pref}`, 907 `Prefer` lines instead of 673, doubled `Conflict`
lines (from merging 1.1 and 1.2 by struct, not by text), and no
`%define _project/_repository/_is_this_project/_is_in_project` header.
OBS reference: `BSSched/ProjPacks.pm:getconfig` concatenates the prjconf
*text* of every path element (reverse path order) with those `%define`
headers, then calls `Build::read_config($arch, ...)`. The worker gets the same
concatenated text, **including** the `Macros:` sections.

### 1.6 The worker is not hermetic and ignores the scheduler's result
`pkg/worker/sandbox.go:buildKVMArgs` passes `--repo http://127.0.0.1:8088/`
**first** and the project's own repo snapshot **last**, then lets `build`
re-expand dependencies itself with `queryrepo` (about 22 s per job). The
scheduler's `ChrootRPMs` are downloaded, but `build` never uses them. Repo
order is the reverse of OBS (own repo first, then path).
OBS: `bs_worker` writes an exact `--rpmlist` (around `bs_worker:4156`) from the
buildinfo `bdep` list. There are no network repos during the build.

### 1.7 Missing release numbering and source fidelity
- Built RPMs carry `Release: 0` (`graphviz-12.2.1-0.x86_64.rpm`). The prjconf
  demands `Release: <CI_CNT>.<B_CNT>`. OBS keeps checkin and build counters per
  package (`BSSched/BuildJob.pm:nextbcnt`) and passes `--release`.
- `cloneGitSources` does `clone --depth 1 --branch X` then
  `checkout <sha>`, and ignores the error. If the pinned commit is not the
  branch tip, the build silently uses the wrong sources (not SLSA-provenance
  safe).
- `_multibuild` is not supported: only the default (empty) flavor builds,
  flavors are never scheduled, and `excludebuild:<pkg>:<flavor>` flags match
  nothing. 84 of 779 cloned packages have one (systemd, gcc13 with 26
  flavors, cmake/gettext `mini` flavors needed for bootstrap).
  The recipe is hardcoded as `<name>.spec` in every manifest.
- No `--disturl`, `--buildflavor`/`--obsmultibuild`, or `--vcs`.

### 1.8 Status and event plumbing
`pkg/cli/project.go:fetchProjectPackagePhases` replays the whole
`bsgo.status` stream under a 10 s timeout on every call. Counts are wrong as
soon as the stream gets large.

### 1.9 Hardcoded values to remove (found with grep)
`cmd/bsgo-server/main.go:330` (`"standard","x86_64"` startup cascade),
`pkg/worker/sandbox.go:531,535,699,703,886,890,1035,1039` (target/arch
defaults), `pkg/solv/builder.go:53`, `pkg/cli/tui.go:246,270,361,653,771`,
`pkg/cli/package.go:293`, `cmd/bsgo-worker/main.go:84`,
`pkg/scheduler/scheduler.go` invented prefer list,
`calculatePriority`/`Resources` constants (ignore `pkg.Resources`), and
`Destination.S3Bucket: "bsgo-artifacts"` in `createFallbackJob`.

### 1.10 Publisher keeps POSIX state
`Publisher.workDir/<prj>/<repo>/<arch>` accumulates RPMs on local disk. Old
versions of a rebuilt package are never removed. This conflicts with the
"no shared POSIX" mandate and blocks running more than one publisher.

---

## 2. Target design (OBS concepts mapped to bsgo)

| OBS concept | bsgo component (new/changed) |
|---|---|
| prjconf text + `getconfig` concatenation | `pkg/buildconfig` (new): structured scoped config in `project.yaml` (D1); `Render` per path element → `Layer(path)` as text → `Parse(arch)` → `*Config` |
| `Build::parse` (spec/multibuild) | `pkg/recipe` (new): Go port of `Build/Rpm.pm:parse` subset + `_multibuild` |
| `Build::expand` | `pkg/solv` expander rewritten on libsolv with OBS semantics |
| `:full` + `.bininfo` per package | S3 "build repo": `bins/<prj>/<repo>/<arch>/<pkg>/` + `bininfo.json`; per-prpa `full.solv` (content-addressed) |
| `bs_sched` checker per prpa | `pkg/scheduler/checker` (new): stateless pass per (project, repo, arch); prpa ownership via NATS KV lease (horizontal scaling) |
| `:schedulerstate`, packstatus | Postgres `package_build_state` table (+ `bsgo.status` events as notifications only) |
| buildinfo (`bdep` list) | `BuildJob.BuildInfo`: exact bdeps (name, evr, arch, s3 key, sha256, preinstall/vminstall/runscripts flags), config text, release, disturl |
| `bs_worker` `--rpmlist` | worker downloads exact bdeps into a job-local dir and runs `build --rpmlist` (no `--repo`) |
| DoD / remote path repo | `pkg/dod`: ingest remote `primary.xml` once per repomd checksum → solv blob cached in S3/PG; fetch binaries on demand |

Horizontal scaling remains: checkers are partitioned by prpa, workers pull
jobs, publishers lock per prpa in NATS KV, and all binaries live in S3. No
component needs a shared filesystem.

---

## 3. Decisions (AGENTS.md §1)

- **D1. prjconf model (DECIDED 2026-11-15, ADR 0010, spec 01 §1): one structured `project.yaml`**
  holding both meta and build config. prjconf text is a *render target*, not
  the source of truth. Rules:
  - Visibility scopes map to OBS `getconfig` semantics:
    `public_config` = applies to every consumer that has this project in its
    path; `protected_config` = applies only when `%_is_in_project` is 1 (this
    project or a `this:*` subproject); `private_config` = applies only when
    `%_is_this_project` is 1. `%ifarch` maps to `arch_config.<arch>`, and
    `%_repository` conditionals map to `targets.<repo>.config`.
  - Two separate macro namespaces: `defines:` (map; prjconf top-level
    `%define`, visible to directive expansion such as `Prefer: %{lua_pref}`)
    and `macros:` (opaque text emitted inside `Macros:`/`:Macros`. rpm
    evaluates it at build time, so `%if` inside it stays verbatim).
  - Per-field merge semantics are documented and implemented exactly:
    append (Prefer, Ignore, FileProvides, Conflict, Substitute, ...), override
    (Release, Type, Repotype, ...), negation/reset (`!pkg`, `Preinstall!:`).
    Layering across path projects reproduces OBS text concatenation order
    (reverse path, own project last).
  - `raw_config` (verbatim prjconf fragment) per scope is the escape hatch
    for constructs that do not map cleanly (`%if 0%{?is_stage_project}`,
    `%if 0%{?bootstrap_stage}`, arbitrary user-macro conditionals). The
    converter must **never** flatten `%if` branches. It either maps a
    construct or moves it to `raw_config` and reports it.
  - **Acceptance gate (round-trip oracle):** for each arch × repo × Consumer Project
    (Consumer Project = self, a subproject, a foreign project), the parsed result of
    `render(project.yaml, ctx)` equals the parsed result of
    `getconfig(original _config, ctx)`. Requires updating spec
    `docs/specs/01-manifest-schemas.md` and an ADR 0006 amendment.
- **D2. Expander/spec parser (DECIDED 2026-11-15): native, no Perl at runtime.**
  Spec preprocessor and config parser in Go, expander in C on libsolv inside
  `pkg/solv`. Scope: **control plane only** (scheduler, indexer, CLI). No
  control-plane binary or image may depend on Perl.
  **obs-build stays on workers as the build engine** (`/usr/bin/build`,
  init_buildsystem, chroot/KVM, rpmbuild; WP5 calls it with `--rpmlist` like
  `bs_worker`). It is battle-tested and is not being replaced. The obs-build
  Perl modules (`/usr/lib/build/Build*.pm`) also serve as the permanent
  dev/CI parity oracle (build tag `oracle`), pinned to the same obs-build
  version deployed on workers. Bumping that version requires re-running the
  oracle suite.
- **D3. Build state store (DECIDED 2026-11-15): PostgreSQL** table
  `package_build_state` (WP0). NATS carries only notifications/events, never
  the source of truth for state.
- **D4. Bootstrap mode (DECIDED 2026-11-15).** `SUSE:SLFO:1.1` (via the HTTP
  DoD URL) is a **Bootstrap Dependency** only: a path element *after* the own
  repo. Once every package has been built once, remove it and let
  `rebuild: transitive` converge (the standard OBS approach). 1.1 binaries
  must never end up in the 1.2 build repo or published repo.

---

## 4. Work packages

### WP0. Stop the bleeding and get observability (small, do first)
Depends on: none.
Tasks:
1. Persist build state: migration `package_build_state(project, repo, arch,
   package, flavor, state, details, source_commit, meta, bcnt, job_id, updated_at)`,
   PK `(project, repo, arch, package, flavor)`. States: `scheduled, building,
   succeeded, failed, unresolvable, blocked, broken, excluded, disabled`
   (OBS names). Writers: the scheduler (unresolvable/blocked/scheduled) and
   the job-status consumer (building/succeeded/failed).
2. `bsgo project status` reads this table (with `--repo`, `--arch`, `--state`
   filters, `--json`, and `details` per package). Delete the stream replay in
   `pkg/cli/project.go`.
3. Remove the startup cascade at `cmd/bsgo-server/main.go:326-338` and the
   `HasPrefix` matching in `cascade.go:checkDependencyMatch`. Until WP4
   lands, triggers come only from explicit commands.
4. Remove the invented prefer list in `scheduler.go:buildJobForArch` and the
   `"glibc"/"bash"` heuristic in `c_bridge.c:select_provider`.
5. Remove the hardcodes listed in §1.9. Missing target/arch must be an error.
Acceptance: `bsgo project status SUSE:SLFO:1.2 --json` counts match a
`SELECT state, count(*)` on the table. No `"standard"`/`"x86_64"` literals
outside tests, demo, and docs (`grep` check in CI).

### WP1. Build config: structured YAML model, renderer, OBS layering, parser
Depends on: D1.
Files: new `pkg/buildconfig/` (`render.go`, `layer.go`, `parse.go`,
`macros.go`), `pkg/manifest` (add `defines`, `raw_config`, documented merge
semantics; remove struct `Merge` duplication bugs),
`pkg/cli/project.go` (`project config show --arch --repo --consumer
--rendered`, non-interactive, `--json`).
Tasks:
1. Extend the manifest model per D1: `defines` separate from `macros`,
   `raw_config` per scope (public/protected/private, per target, per arch).
   Each field carries its merge kind (append/override/negate).
2. `Render(prj, repo, arch, consumerProject) string` produces prjconf text
   for one path element: scope selection by Consumer Project (public always;
   protected if consumer == prj or has prefix `prj:`; private if
   consumer == prj), then target and arch overlays, `defines` as `%define`
   lines first, directives, `raw_config`, and `Macros:` last.
3. `Layer(consumer, repo, path []PathElem) string` ports
   `BSSched/ProjPacks.pm:getconfig`: render each path element, concatenate
   in reverse path order with `%define _project`, per element
   `%define _repository`, `_is_this_project`, `_is_in_project` (needed by
   `raw_config` fragments), and the `FromScratch:` reset. Two outputs:
   (a) scheduler text with `Macros:` sections stripped, (b) worker text with
   `Macros:` kept (that is what `build --dist` receives). Merging happens
   **as text, then parse**. Never merge Go structs (this is the source of
   the 907-Prefer / doubled-Conflict bug).
4. `Parse(text, arch) (*Config, error)` ports `Build.pm:read_config`:
   `%define/%undefine/%if/%ifarch/%ifnarch/%else/%endif`, `%{?x:...}`,
   `%{!?x:...}`, `0%{?x}` and expression evaluation as in
   `Build/Rpm.pm:expr`; directives `Preinstall, VMinstall, Runscripts,
   Required, Support, Keep, Prefer, Ignore, Substitute, Conflict,
   FileProvides, ExpandFlags, BuildFlags, PublishFlags, Release, Type,
   Repotype, Patterntype, Hostarch, Optflags, Constraint, ExportFilter, Order`.
   Include `!` negation (`Support: !foo`), the `Preinstall!:` reset forms, and
   case-insensitive keys.
5. Parser parity test `test/oracle/buildconfig_test.go` (build tag
   `oracle`, dev/CI only, per D2): runs `perl -I/usr/lib/build -MBuild
   -MData::Dumper -e 'print Dumper(Build::read_config(...))'` on fixtures
   and compares the normalized JSON to `Parse`. Fixtures: SLFO 1.1 + 1.2
   original `_config` (copy into `test/fixtures/prjconf/`) for arches
   x86_64, aarch64, i586.
6. **Round-trip test (the D1 gate)** `test/oracle/roundtrip_test.go`: for
   SLFO 1.1 and 1.2, for each arch × repo (`standard`, `images`,
   `containerfile`) × Consumer Project (self, `SUSE:SLFO:1.2:Sub`, `home:x`), assert
   `Parse(Layer(yaml-model))` == oracle `read_config(getconfig(original
   _config))`. Also run a pure-Go variant without Perl
   (`Parse(Layer(yaml))` == `Parse(getconfig(original))`) in normal CI so
   regressions are caught even where Perl is absent.
Acceptance: parser parity for all fixtures (prefer/ignore/substitute/conflict/
fileprovides/preinstall/support/required/release/buildflags/expandflags/
defines). The round-trip test passes for SLFO 1.1 and 1.2. The worker writes
the layered worker text verbatim to `.build.config`. The old
`config_render.go` and the `ProjectConfig.Merge` path are deleted.

### WP2. Recipe parsing (spec + `_multibuild`)
Depends on: WP1.
Files: new `pkg/recipe/` (`spec.go`, `macros.go`, `multibuild.go`,
`types.go`), `pkg/manifest` (remove the hardcoded `<name>.spec` assumption;
`recipe` becomes optional).
Tasks:
1. Port the dependency-relevant subset of `Build/Rpm.pm:parse`: macro
   engine (`%define`, `%global`, `%undefine`, parametric macros, `%{expand:}`,
   `%{?..}`, `%if/%else/%elif/%endif`, `%bcond_with(out)`, `%{with x}`),
   `Name/Version/Release/Epoch`, `BuildRequires`, `BuildPrereq`,
   `BuildConflicts`, `ExclusiveArch/ExcludeArch`, `%package` subpackages,
   `#!BuildIgnore`, `#!BuildRequires`, `#!BuildConstraint`, `#!BuildTarget`,
   and `%(...)` as "cannot expand" like the oracle does. Macros come from the
   parsed config's `Macros:` text (the scheduler variant still needs the
   rpm macros that `Build::parse` uses; follow what `Build::parse` receives in
   bs_srcserver: `$config` from the full getconfig *with* macros).
2. `_multibuild` (source of truth: the file in the package git repo at the
   pinned commit, never `project.yaml` or the package manifest). Port:
   - **Expansion** (`BSSrcServer/Multibuild.pm:addmultibuildpackages`): each
     `<flavor>` or `<package>` entry (equivalent element names) yields a
     buildable unit `pkg:flavor`. `buildemptyflavor="false"` drops the plain
     `pkg` unit. Package identity becomes `name[:flavor]` everywhere (state
     table, jobs, S3 build repo keys, CLI).
   - **Recipe selection** (`bs_srcserver:findfile`, with the flavor as the
     lookup name): `<flavor>-<repo>.spec`, then `<flavor>.spec`, then the
     normal fallback (main spec). Example: gcc13 flavors use
     `cross-*-gcc13.spec`, systemd `mini` reuses `systemd.spec`.
   - **Parsing** (`bs_srcserver` around line 1938): parse with
     `buildflavor` set, so `@BUILD_FLAVOR@` is substituted before deps are
     read (`Build/Rpm.pm:913`). Each flavor has its own deps.
   - **Config**: `BuildFlags: excludebuild:<pkg>[:<flavor>]` and
     `onlybuild:` apply per unit.
   - **Worker** (WP5): pass `--buildflavor=<flavor>` and
     `--obspackage=<pkg>` (`bs_worker:4275`). obs-build does the rest.
   - Oracle test cases: systemd (mini), gcc13 (per-flavor spec), a
     `buildemptyflavor="false"` package, and an `excludebuild` flavor.
3. Recipe selection rules from `BSSched`/srcserver: `<pkg>.spec` if present,
   else the single spec, else `kiwi/Dockerfile/...` by type.
4. Source fetch at a pinned commit: the scheduler needs the recipe files, not
   the tarballs. Add `pkg/gitops` "recipe fetcher": `git fetch --depth 1
   origin <sha>` (no branch tip assumption), read `*.spec` and `_multibuild`,
   cache by `(url, sha)` in S3 (`recipes/<sha256(url)>/<sha>/...`).
5. Oracle test: `perl -I/usr/lib/build -MBuild -e 'Build::parse($cf,$spec)'`
   vs `recipe.Parse` over a corpus of 300 SLFO specs fetched at pinned SHAs
   (script `hack/oracle/fetch-spec-corpus.sh`; store under
   `test/fixtures/specs/`, LFS or a generated, git-ignored cache).
Acceptance: ≥ 99% exact parity on `deps`, `subpacks`, `exclarch`, `badarch`
over the corpus. Every mismatch is listed in `test/oracle/known_diffs.md`
with a reason.

### WP3. OBS-compatible expander + real binary indexes
Depends on: WP1. Can run in parallel with WP2.
Files: `pkg/solv/c_bridge.c`, `pkg/solv/solv.go`, `pkg/solv/expand.go`
(new), `pkg/solv/index.go` (new), `pkg/publisher` (solv cache),
`pkg/dod` (ingest).
Tasks:
1. Dependency strings → libsolv deps with `pool_parsedeps`-equivalent
   handling: `name op evr`, rich deps `( ... )`, file deps. Port the
   parse logic from libsolv `testcase_str2dep` (available in libsolvext) or
   use it directly.
2. Rewrite `bsgo_expand` to follow `/usr/lib/build/Build/Expand.pm:expand`
   exactly: `Substitute` (applied to input), `Ignore` global and `a:b`,
   `Prefer` positive, negative and `a:b`, `Conflict`, `ExpandFlags`
   (`keepfilerequires`, `preinstallexpand`, `ignoreconflicts`),
   `have choice for X needed by Y: a b` errors with the same text as OBS,
   `nothing provides X needed by Y`, and conflict errors. Return the
   **ordered** list plus per-package "why" data for debugging (`--debug` in
   `bsgo package explain`).
3. Path layering: `NewPathPool(arch, []RepoBlob)` where the order is the
   project path order (own repo first). Hide lower-priority solvables whose
   name already exists in a higher-priority repo (OBS
   `Checker.pm:createpool` semantics).
4. Pools are long-lived and cached per (prpa, path content hashes). A resolve
   must never re-parse primary.xml. Invalidate when any repo blob hash changes.
5. Real binary index: at upload time the worker extracts header data
   (name, epoch, version, release, arch, provides, requires, conflicts,
   obsoletes, sha256 of the RPM file, sourcerpm) from each RPM. Use `rpm -qp --qf`, or a
   maintained Go RPM reader that passes AGENTS §2. It uploads
   `bininfo.json` next to the binaries. The indexer (WP4) builds per-prpa
   `full.solv` from the bininfo files. Delete `ParseRPMFilename`-based
   indexing and `MergeAndDumpSolv` append semantics.
6. DoD: `pkg/dod` ingests a remote rpm-md repo once per `repomd.xml`
   checksum into a solv blob (stored in S3 under
   `dod/<sha256(url)>/<repomd-checksum>.solv`, pointer in Postgres). Filter
   by arch with libsolv, not by blanking provides.
7. Oracle test: `/usr/lib/build/expanddeps --dist <cfg> --depfile <deps>
   --archpath x86_64:i686:i586:noarch <spec>` over the WP2 corpus against the
   SLFO 1.1 repo. Generate the depfile with `/usr/lib/build/createrepomddeps`.
Acceptance: ≥ 99% identical expansion sets (order may differ inside cycles)
on the corpus, with all diffs documented. Resolve latency is ≤ 50 ms p95
with a warm pool (benchmark test).

### WP4. Scheduler checker (the `bs_sched` equivalent)
Depends on: WP0, WP2, WP3.
Files: new `pkg/scheduler/checker/` (`checker.go`, `order.go`, `meta.go`,
`cycles.go`), `pkg/scheduler/indexer.go`, `cmd/bsgo-server/main.go`
(wiring), `pkg/proto/v1/messages.go` (BuildInfo).
Tasks:
1. **prpa ownership**: one checker goroutine per (project, repo, arch) holds
   a NATS KV lease. Multiple server replicas split prpas between them.
   Events (source change, binaries published in any path prpa, config
   change, worker finished) mark the prpa dirty. The checker runs a full pass
   per dirty prpa and debounces.
2. **Pass algorithm** (port of `BSSched/Checker.pm:setup → preparepool →
   expandandsort → checkpkgs`):
   a. concatenated config (WP1) for the prpa; b. parsed recipe info (WP2)
      for every package/flavor, cached by `(source_commit, configHash)`;
   c. path pool (WP3); d. expand every package → `unresolvable` with OBS
      text, or a bdep list; e. compute the in-prpa dependency graph (a
      package depends on a source in the same prpa that produces one of its
      bdeps), SCCs, and build order (`cycsort/handlecycle`);
   f. per package in order: `excluded/disabled` (flags, ExclusiveArch,
      `excludebuild:`) → `blocked` if any bdep's producer in the same prpa
      is scheduled/building/blocked (respect target `block: all|local|never`)
      → compute `meta` = sha256(source_commit + sorted bdep sha256s), using
      genmetaalgo 1 cycle rules from `BuildJob.pm:metacheck` →
      `scheduled` only if meta differs from the last successful build's meta
      (or there is none) → else `succeeded`.
   g. Write state rows in one transaction, create jobs for newly
      `scheduled` packages, and cancel obsolete jobs (`killunwantedjobs`).
3. **BuildInfo** in the job: concatenated worker config text, exact bdep
   list (S3 key or DoD URL + sha256 per binary, with
   preinstall/vminstall/runscripts/notmeta flags from the config's
   `Preinstall/VMinstall/Runscripts/Support/Required` expanded per
   `BuildJob.pm:create`), `release` (`<CI_CNT>.<B_CNT>` substitution per the
   `Release:` directive, counters in Postgres; `nextbcnt` semantics),
   `disturl` (embeds source_commit), `flavor`, `source_commit`, `meta`.
4. **Indexer**: on a job-finished event, atomically replace the package's
   binary set in the prpa build repo
   (`bins/<prj>/<repo>/<arch>/<pkg>[:flavor]/`), rebuild `full.solv` from
   all `bininfo.json` (with OBS "useforbuild" filtering), write it
   content-addressed, and publish `bsgo.events.repo.changed.<prpa>` →
   dependent prpas become dirty.
5. Replace `CascadeEngine`, `MemoryGatekeeper` and the trigger-driven
   `ProcessTrigger` path with the checker. Keep `bsgo package rebuild`
   (sets a forced rebuild flag that the checker honors).
Acceptance (integration test with podman-compose: NATS + Postgres + Garage,
fixture project of about 8 small packages with an A→B→C chain, one cycle, a
multibuild flavor and an unresolvable package): build order is respected, no
duplicate builds, the cycle builds once per change, and states plus reason
texts match OBS wording.

### WP5. Hermetic worker execution
Depends on: WP4 (BuildInfo).
Files: `pkg/worker/sandbox.go` (split into `buildinfo.go`, `fetch.go`,
`obsbuild.go`, `collect.go` to meet funlen), `pkg/worker/worker.go`.
Tasks:
1. Download every bdep from S3 or DoD into `jobs/<id>/bdeps/` (worker-local
   cache keyed by sha256 is fine; it is not shared), verify sha256, and fail
   the job on mismatch. Drop the `"mock-sha256-digest"` placeholder.
2. Write `rpmlist` exactly as `bs_worker` does (around `bs_worker:4156`):
   `name path` lines plus `preinstall:`, `vminstall:`, `runscripts:`,
   `dist:` lines. Call `build --rpmlist <file> --dist <cfg> --release <rel>
   --disturl <url> [--buildflavor <f> --obspackage <pkg>]
   --arch <archpath>`, with **no `--repo`**. No network inside the build
   (KVM `-net none` / chroot without network).
3. Source checkout at the exact commit (`git fetch --depth 1 origin <sha>`,
   then `checkout FETCH_HEAD`). Fail the job if that fails. Run
   `git lfs pull` with errors checked. Record `source_commit` for
   provenance.
4. After the build: extract `bininfo.json` (WP3.5), upload binaries,
   logfile and `_statistics` to `artifacts/<jobid>/`, and send
   job-finished with the list. **Do not** call the publisher directly. The
   indexer (WP4.4) integrates binaries, and publishing is separate (WP6).
5. Respect `BuildFlags: logidlelimit`, job timeout from `pkg.Resources`,
   and `--jobs` from `Resources.VCPUs`. Report `ABORTED` vs `FAILED`
   correctly.
Acceptance: the job log shows no `queryrepo` or `expanding package
dependencies` against remote repos. The build root contains exactly the
bdeps. A golden test confirms that `rpmlist` content matches what
`bs_worker` would produce for the same buildinfo fixture.

### WP6. Publisher without POSIX state
Depends on: WP4 (build repo layout).
Files: `pkg/publisher/*`.
Tasks:
1. A publish of prpa X reads the current binary set from the build repo
   index (S3), applies `ExportFilter` and the debug-filter policy (no
   `-debuginfo`/`-debugsource` when the target's `debuginfo: false`), runs
   `createrepo_c` in a **temp dir that is deleted afterwards**, and uploads
   repodata atomically (`repomd.xml` last). Remove the persistent `workDir`
   accumulation.
2. Publishing is debounced per prpa, and the lock is a NATS KV lease (not
   `LocalLockManager`) so several publishers can run.
3. The published repo is not the solver's input. Only the build repo
   (`full.solv`) feeds the scheduler.
Acceptance: two publisher replicas run concurrently without corruption
(integration test). A rebuilt package's old NEVR disappears from the
published repo.

### WP7. SLFO import tooling fixes
Depends on: WP1, WP2.
Files: the converter (wherever `project.yaml` for SLFO is generated, likely
`hack/`; confirm with the human), `docs/specs/01-manifest-schemas.md`.
Tasks:
1. Rewrite the prjconf → `project.yaml` conversion per D1. Use the WP1
   parser to tokenize `%if` blocks instead of flattening them. Map
   `_is_in_project` to protected, `_is_this_project` to private, `%ifarch`
   to `arch_config`, `%_repository` to `targets.<repo>.config`, top-level
   `%define` to `defines`, and `Macros:` bodies verbatim to `macros`.
   Anything else (e.g. `%if !0%{?is_stage_project}` around `Release:`) goes
   to `raw_config` and is listed in the converter's `--json` report. The
   converter output must pass the WP1.6 round-trip test before it is
   committed. Remove the invented `%debug_package %nil` (no debuginfo is
   already the default when `build` is run without `--debug`). Remove
   `macros_file: _config`: the converted `project.yaml` must be fully
   self-contained, and bsgo never reads a prjconf file at runtime. The
   original OBS `_config` is only converter **input** and a **test fixture**
   (`test/fixtures/prjconf/`, used by the WP1.6 round-trip test). It is not
   committed to the project repo next to `project.yaml`.
2. The target path for bootstrap is
   `[own, {url: http://<host>:8088/, name: "SUSE:SLFO:1.1/standard"}]` in
   that order. A DoD element needs a stable name for `_repository`-style
   config concatenation.
3. Manifests: drop `recipe: <name>.spec` (auto-detected in WP2.3), and drop
   uniform `resources` defaults unless the source has `_constraints`.
4. `import-to-bsgo.sh`: stop swallowing errors (`2>/dev/null || true`).
   Use `--json` and report failures.

### WP8. Bootstrap runbook (operator task, after WP0–WP7)
1. Reset state: purge the `bsgo.status` stream, truncate
   `package_build_state`, empty the `bins/SUSE:SLFO:1.2/` prefix, and remove
   stale `/var/tmp/bsgo-worker/jobs/*` (and `cache/s3_repos`).
2. Re-import SUSE:SLFO:1.2 with the Go importer. Keep the HTTP server on
   `~/Devel/packages/1.1/standard` running.
   ```
   bsgo project import-obs-git --repo ~/Devel/packages/SLFO --ref origin/slfo-1.2 \
     --meta <SUSE_SLFO_1.2.meta.xml> --out /tmp/slfo12-import \
     --bootstrap-url http://<host>:8088/ --bootstrap-name SUSE:SLFO:1.1/standard \
     --bootstrap-targets standard --json
   bsgo project apply /tmp/slfo12-import --json
   ```
3. `bsgo project status SUSE:SLFO:1.2 --arch x86_64 --state unresolvable
   --json` before any build. Expected: a small residual (packages that are
   genuinely new in 1.2 and whose providers are not built yet show as
   `blocked`, not `unresolvable`). Triage the remainder by `details`.
4. Let the checker run. Watch `succeeded` grow monotonically and confirm no
   package builds twice without a meta change.
5. When `scheduled + building + blocked == 0`: remove the 1.1 path element
   (`bsgo project dependency remove SUSE:SLFO:1.2 --target standard
   --name SUSE:SLFO:1.1/standard --yes`), then let `rebuild: transitive` converge (the second pass rebuilds
   everything that built against 1.1 binaries).
6. Done criteria: all non-excluded x86_64 packages are `succeeded`, or
   `failed` with a log reason that also fails in OBS for SLFO:1.2. The
   published repo installs a minimal system
   (`zypper --root /tmp/t --non-interactive in patterns-base-minimal_base`
   or equivalent).

---

## 5. Dependency graph of work packages

```
WP0 ──────────────────────────────┐
D1 → WP1 ──┬─→ WP2 ──┐            │
           └─→ WP3 ──┴─→ WP4 ─────┴─→ WP5 ─→ WP6 ─→ WP8
           WP1+WP2 ─→ WP7 ───────────────────────────┘
```

Parallelizable: WP0 ∥ WP1. WP2 ∥ WP3. WP5 ∥ WP6 once WP4's message
contracts are merged (land the `BuildInfo` struct + spec doc first as a
separate PR).

## 6. Guidance for implementing agents

- **No MD5 identities.** OBS uses `srcmd5`, `hdrmd5`, `verifymd5` and an md5
  `meta`. bsgo uses git-native, content-addressed ids instead: source
  identity = git commit object id (`source_commit`, SHA-1 or SHA-256 repos,
  stored as is); binary identity = sha256 of the RPM file; `meta` =
  sha256(source_commit + sorted bdep sha256s). Never compute MD5.

- Port **behavior**, not structure. Read the cited Perl sub, write a table of
  inputs → outputs from the oracle, then implement in Go. Keep the oracle
  table as the unit test.
- Error texts that users see (`nothing provides`, `have choice for`,
  `conflict for providers of`, `blocked: a, b`) must match OBS wording. Agents
  and humans grep for them.
- Keep cgo minimal: dependency parsing and expansion in C on libsolv, and
  everything else in Go.
- Each PR stays under about 600 changed lines of non-test code. Split WP3
  and WP4 into several PRs (expander, layering, index, DoD; checker
  skeleton, ordering, meta, cycles, indexer).
- Update `docs/specs/02-nats-message-contracts.md` in the same PR as any
  message change.
