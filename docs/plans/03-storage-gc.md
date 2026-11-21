# Plan: Garbage collection of build repository indexes and old build artifacts

Status: G1, G2, G3 and G5 implemented; G4 (operator cleanup) pending
Audience: implementation agents (Sonnet) + human reviewer
Depends on: plan 01 (indexer, build repository, publisher), plan 02 (signing stage)

Follow the rules of plan 01 §0 and §6 and AGENTS.md (quality gates, table-driven
tests with real in-memory types, funlen/gocognit limits, no suppression, plain
logs, CLI with `--help` examples, `--json`, `--dry-run`, `--yes`). OBS behaviour
is the reference where it applies.

---

## 1. Problem (measured on aisrv, 2026-11-21)

Nothing in S3 is ever deleted. Bucket `bsgo-artifacts`:

| Prefix | Objects | Size | Content |
|---|---|---|---|
| `buildrepo/` | 10,240 | 6.7 GiB | every version of every prpa index: `<hash>.solv` (~1.1 MB for SLFO 1.2 x86_64) + `<hash>.entries.json` (~1.3 MB), one pair per finished build |
| `artifacts/` | 25,265 | 46.8 GiB | build results per unit; old releases stay next to new ones (e.g. `patch-2.7.6-160000.1.5` ... `1.16` all present) |
| `repos/` | 9,012 | 16.5 GiB | published repositories (publisher already deletes binaries it no longer publishes) |
| `backup/` | 6,234 | 8.1 GiB | not produced by bsgo code; out of scope, report only |
| `recipes/`, `dod/` | small | | out of scope |

Only the current index of each prpa is needed (pointer `build_repo_index.solv_key`
/ `entries_key` in Postgres), and only the binaries that the current index
references. OBS keeps exactly that: `:full` holds the current binaries, and
integrating a package build replaces the package directory, so old releases
disappear (`BSSched/BuildJob.pm` `jobfinished`, `BSSched/BuildRepo.pm`).

## 2. Decisions to keep (do not change)

- Index versions stay **content-addressed and immutable**; the pointer swap in
  Postgres stays the only commit point. GC only removes objects that are not
  reachable from any pointer.
- **Grace period** before deleting anything that just became unreachable, so
  readers that loaded a pointer a moment ago (checker pass, publisher run,
  queued or running jobs downloading bdeps) never see a missing object.
- GC must be safe with several server replicas: only one GC run at a time per
  bucket (use the existing lock/lease mechanism the indexer uses, see
  `pkg/scheduler/indexer` "locked by another writer", or a NATS KV lease; find
  and reuse what exists).
- GC never deletes the current pointer's objects, never deletes `repos/`
  content (the publisher owns it), never touches `backup/`.

## 3. Work packages

### G1. Index GC (`buildrepo/`) — required

Files: new `pkg/gc` (or `pkg/scheduler/buildrepo/gc.go`; choose the deep-module
boundary), `pkg/storage` (list pointers), `cmd/bsgo-server` (background loop),
`pkg/cli` (admin command).

Reachable set:
- for every row of `build_repo_index`: `solv_key` and `entries_key`;
- keep in addition the last `N` versions per prpa (default 3) for debugging,
  ordered by object LastModified;
- never delete objects younger than the grace period (default 24h).

Everything else under `buildrepo/<prpa>/` is garbage: `<hash>.solv` and
`<hash>.entries.json`. Delete both of a pair together. Objects under
`buildrepo/` whose prpa has no `build_repo_index` row at all (removed projects,
repositories or architectures) are garbage too, after the grace period.

Interface sketch (adapt to the code base):

```go
type Plan struct {
    Prpa      string
    Keep      []string // current + last N + young
    Delete    []string
    FreeBytes int64
}
func PlanIndexGC(ctx, store, objects, now, opts) ([]Plan, error)
func Apply(ctx, objects, plans, dryRun bool) (Result, error)
```

S3 access: `S3Storage.ListObjects` and `DeleteObject` exist
(`pkg/storage/s3/client.go`); add batch delete (S3 DeleteObjects, up to 1000
keys per call) if missing, with the in-memory test store implementing the same
interface.

Acceptance:
- table-driven tests with an in-memory object store and store: current pair
  kept; last N kept; young objects kept; orphan prpa objects deleted after grace;
  a pointer swap between planning and applying does not delete the new current
  pair (re-read pointers right before deleting, or plan only objects older than
  grace which the new pointer cannot reference);
- dry run deletes nothing and reports the same plan.

### G2. Artifact GC (`artifacts/`) — required

Reachable set for unit directory `artifacts/<project>/<repo>/<arch>/<unit>/`:
- every RPM key listed in the **current** `entries.json` of every prpa (this
  includes binaries exported to other architectures and import units; check
  `pkg/scheduler/indexer` export/import routing for keys outside the unit's own
  directory);
- the unit's current metadata files: `bininfo.json`, `_slsa_provenance.json`,
  `_statistics`, `build.log` (single objects, overwritten per build);
- debug and source RPMs are not in the build repository index (UseForBuild): keep
  the ones belonging to the current build of the unit. Determine the current
  build's file list from the unit's current `bininfo.json` (it lists every RPM of
  the build, including `-debuginfo`/`-debugsource` and `.src.rpm` if present) —
  verify against `pkg/worker/collect.go` what is uploaded and listed;
- anything younger than the grace period;
- files referenced by jobs that are queued or running: their bdeps are resolved
  from the index at scheduling time. The grace period covers the normal case;
  additionally skip keys of bdeps of jobs the dispatcher still tracks if that is
  cheap (`Dispatcher.Tracks`), otherwise document the grace-period assumption.

Everything else in the unit directory (older releases' RPMs) is garbage.
Directories of units that no longer exist in the project (package removed) are
garbage after the grace period.

Acceptance: table-driven tests covering: old release deleted, current kept,
exported binary referenced from another arch kept, debuginfo of the current
build kept, debuginfo of an old build deleted, young objects kept, removed unit
deleted after grace, dry run.

### G3. Scheduling and CLI

- Server background loop: run GC every `--gc-interval` (default 6h in this plan, implemented as `0` = disabled until the G4 dry run is reviewed; `0`
  disables), with `--gc-grace` (default 24h) and `--gc-keep-index-versions`
  (default 3). One plain log line per run per prefix:
  `[GC] buildrepo: kept 6, deleted 10234 objects, freed 6.6 GiB in 41s`.
  Errors are logged and the run continues with the next prpa.
- CLI: `bsgo admin gc [--prefix buildrepo|artifacts|all] [--project P]
  [--dry-run] [--json] [--yes]`. Without `--yes` and without `--dry-run` it asks
  for confirmation when interactive, and fails when not interactive. `--json`
  prints the plan/result (per prpa: kept, deleted, bytes) deterministically.
  Rich `--help` with examples. Scope it with an admin token permission
  (`admin:gc`, see `docs/specs/04-access-control-and-auth.md`).

### G4. One-time cleanup on aisrv (operator step, after merge and deploy) - DONE 2026-11-21

Result: dry run first (no overlap between planned artifact deletions and the
current entries.json keys of x86_64/i586), then `--prefix buildrepo --yes`:
deleted 8,102 objects, freed 4.3 GiB (2,448 kept: versions superseded within the
24h grace during the glibc rebuild wave); checker and publisher verified; then
`--prefix artifacts --yes`: deleted 11,901 objects, freed 25.7 GiB (13,968
kept). No missing-bdep errors afterwards. Background loop enabled with
`-gc-interval 6h` in hack/systemd/bsgo-server.service.

1. `bsgo admin gc --prefix all --dry-run --json > gc-plan.json`; review totals
   (expected: `buildrepo/` from 6.7 GiB to a few MB; `artifacts/` roughly to the
   size of the current builds).
2. Back up nothing extra (S3 objects are content-addressed; the Postgres dump
   procedure from earlier deploys is enough for state).
3. `bsgo admin gc --prefix buildrepo --yes`, check the checker and publisher keep
   working for one pass, then `--prefix artifacts --yes`.
4. Record the freed space in this plan.

### G5. Docs

Update `docs/guides/solv-index-explained.md` §7 and the walkthrough: retention
rules, grace period, flags, CLI.

### Implementation summary (G1, G2, G3, G5)

- `pkg/gc`: `PlanIndexGC`, `PlanArtifactGC`, `Apply` (batches of 1000, protected
  set re-read before each plan's deletions), `Collector` (run lease `gc` in the
  coordinator, renewed while running; dry run via `Plan`), in-memory objects for
  tests. `storage.Store.ListBuildRepoIndexes` is the root set; S3
  `DeleteObjects` and the `s3.GCObjects` adapter are in `pkg/storage/s3`.
- Artifact references come from the current `entries.json` of every prpa (global
  key set, so exports and imports are covered by key, not by directory), the
  current `bininfo.json` of each live unit, the metadata names, and the grace
  period. The dispatcher is not consulted (job bdeps are not derivable from job
  IDs); the grace period covers queued and running jobs.
- Review fixes: grace counts from supersession (an index version is deleted
  only when its successor is older than the grace period, and the RPMs listed by
  such versions stay); files of the indexed build stay when a newer bininfo.json
  replaced the indexed one; the plan's objects are listed again before deleting
  and re-uploaded keys are skipped; grace below 1h is rejected; package listing
  errors or a project with pointers but no packages abort that prpa's plan; a
  unit with a build state row exists; `--gc-interval` defaults to 0; the CLI
  deletes exactly the confirmed plan.
- Safeguards beyond the plan: refusal when the pointer table is empty while
  objects exist; a live unit without `bininfo.json` or with an unreadable one is
  kept whole (warning); keys that do not have the
  `artifacts/<prj>/<repo>/<arch>/<unit>/<file>` shape are not touched.
- Server flags `--gc-interval`, `--gc-grace`, `--gc-keep-index-versions`; CLI
  `bsgo admin gc` with `--prefix`, `--project`, `--grace`, `--keep-index-versions`,
  `--dry-run`, `--json`, `--yes`; scope `admin:gc` documented in the access
  control spec.
- Finding: unit directories of flavors are doubled (`gcc13:cross-aarch64-gcc13:
  cross-aarch64-gcc13/`). The checker already passes `Package = "pkg:flavor"` in
  the job and the worker's `artifactPrefix` appends `:<flavor>` again. GC does
  not depend on the name, it works on the directory and on entry keys. Fixing the
  worker needs a coordinated change (the bininfo/entries keys of existing builds
  contain the doubled name) and is a follow-up.

## 4. Out of scope

- `repos/` (publisher-owned), `backup/` (unknown producer), `recipes/` cache,
  `dod/` (one object per DoD repository, overwritten).
- Postgres `build_meta` pruning (already implemented in the checker with a 48h
  grace).
- Worker-local job directories (`/var/tmp/bsgo-worker*/jobs`): separate small
  task (age-based cleanup in the worker), list as follow-up.

## 5. Order

G1 → G3 (with G1 only) → G2 → G3 extended → G5 → G4 (operator).
G1 and G2 are independent once the shared GC skeleton exists.
