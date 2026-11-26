# Plan: Release counters (CI_CNT / B_CNT) from the source history

Status: W1-W5 implemented, W6 not implemented (decision 1), W7-W8 open
Audience: implementation agents (Sonnet) + human reviewer
Depends on: plan 01 (checker, recipe fetch, manifests), plan 03 (coord leases, CLI patterns)

Follow the rules of plan 01 §0 and §6 and AGENTS.md (quality gates, table-driven
tests with real in-memory types, funlen/gocognit limits, no suppression, plain
logs, CLI with `--help` examples, `--json`, `--dry-run`, `--yes`). OBS behaviour
is the reference where it applies; deviations are listed in §3.

---

## 1. Problem (measured 2026-11-26)

SLFO prjconf: `Release: <CI_CNT>.<B_CNT> spec:160000.<CI_CNT>.<B_CNT>`
(`pkg/scheduler/checker/meta.go` `release`, `jobbuild.go:75`). bsgo allocates
both counters in `NextBuildCounters` (`pkg/storage/postgres/checker_state.go`):

- CI_CNT = number of distinct source commits bsgo has seen for
  `(project, package, flavor)`, starting at 1 on first sight, never reset;
- B_CNT = number of build attempts of that commit in `(project, repo, arch)`.

aisrv `build_counters`: 25,254 rows, 2,817 units; `max(ci_cnt)` per unit is 1
for 2,816 units and 2 for one; `max(bcnt)` is 130. So nearly every binary is
`160000.1.<n>`, and two packages that build a binary of the same name and version
produce the same NEVRA with different content. Observed on SUSE:SLFO:1.2:
`kernel-source:kernel-azure` and `kernel-source-azure:kernel-azure` both produce
`kernel-azure-6.12.0-160000.1.1.x86_64.rpm`; the publisher reports name conflicts
and the build index holds two solvables with the same name and EVR. In OBS they
are `160000.41.1` and `160000.2.2`.

## 2. How OBS computes the counters (verified in `~/Devel/open-build-service/src/backend`)

1. **At commit time** (`bs_srcserver` `addrev`, ~740-830) the source server calls
   `getcommitinfo` (~546-588): `findfile($rev, undef, $type, $files)` for the
   types `spec, dsc, kiwi, docker, productcompose` in this order, with the
   **package name** as lookup (multibuild flavors are not consulted, so for
   `kernel-source` this is `kernel-source.spec`). The recipe is parsed with
   `Build::read_config('noarch', <project's own _config>)` (no path layering,
   no `%include` callback). `version` = the parsed Version, `unknown` if none or
   if `multiversion`; `_link` packages are always `unknown`. A start value comes
   from the recipe Release: leading digits (`(\d+)\.<B_CNT>`, `<RELEASE(\d+)>`,
   `^(\d+)`, else 0), adjusted by prjconf `cicntstart`.
2. `BSRevision::addrev_local` (`BSRevision.pm:363`) stores the revision with
   `fdb_add_i2(..., 'vrev', 'version', $version)` (`BSFileDB.pm:304`):
   vrev = 1 + vrev of the **last revision in the package's revision log with the
   same version** (anywhere in the log, not only the previous one), or the start
   value if larger. So vrev restarts at 1 for a new version, and continues where
   it left off when a version comes back (revert of a version bump).
3. Special case (`bs_srcserver` ~818-829): on a known→unknown or unknown→known
   change vrev is at least the previous revision's vrev + 1. A `vrev` cgi
   parameter overrides everything. Links (`BSSrcServer/Link.pm` ~620-631) add the
   link target's vrev (`cicount` `add`, default), keep the link's own (`local`)
   or copy the target's (`copy`); git packages have no links.
4. **At scheduling time** (`bs_srcserver` getprojpack ~1974) `versrel =
   "<version>-<vrev>"`, where *version* comes from the recipe of the unit being
   built (for a multibuild flavor its own spec, e.g. `kernel-azure.spec`) and
   *vrev* from the package revision. `BSSched/BuildJob.pm:974-994`:
   CI_CNT = the part of versrel after the last `-` (i.e. vrev);
   B_CNT = `nextbcnt` (913-935) = 1 + bcnt of the last **finished** build with
   the same versrel in the prpa's history of that packid (flavor-specific),
   maxed with `relsyncmax` (`bcntsynctag`).
5. **scmsync** (`BSSrcServer/Scmsync.pm` `sync_package`, `sync_project`): a
   package whose `scmsync` URL (with commit fragment) changes triggers the
   obs-scm-bridge service, which commits **one OBS revision per trigger**, i.e.
   per observed pin change, not per git commit. Intermediate git commits pushed
   together become one revision. Uncertain (not verifiable here): whether
   SUSE:SLFO:1.2 packages started with revisions copied from SUSE:SLFO:Main
   (copypac keeps vrev), and whether an identical tree creates a new revision.

So in OBS, vrev is a property of an **append-only revision log per (project,
package)**, seeded by whatever history the project had when it was created.

## 3. Decisions

### 3.1 CI_CNT = vrev of a bsgo package revision

- bsgo keeps an append-only revision log per `(project, package)`: a new
  revision is appended **when the package manifest's `(git url, commit)`
  changes** (in `UpsertPackage`, same transaction), like an OBS commit.
- A revision's **vrev** is resolved asynchronously:
  `vrev = max(git_vrev(url, commit), floor + 1, prev_unknown_rule)` where
  - `git_vrev` is the OBS rule applied to the **first-parent git history** of
    the commit (3.2): deterministic from git alone, so a fresh deployment gets
    the same numbers and the numbers reflect the history before bsgo existed;
  - `floor` = max vrev of earlier resolved revisions of the same `(project,
    package)` **with the same version** (absent: no floor). This is OBS's
    `fdb_add_i2` over bsgo's own log and makes vrev monotonic per version under
    force-push, branch switch, re-pin to an older commit and history rewrites;
  - `prev_unknown_rule`: if this or the previous revision has version `unknown`
    and the versions differ, at least previous vrev + 1 (OBS ~818-829).
- vrev is per package (all flavors share it), as in OBS. Version for the vrev
  comes from the package's **main recipe** (OBS `getcommitinfo`), not from the
  flavor's recipe.
- Once resolved, a revision's vrev never changes (except by an explicit admin
  command, 3.7). Later prjconf changes do not alter stored versions (OBS stores
  the version at commit time too).

### 3.2 git_vrev: OBS's rule over first-parent history

For the first-parent chain `c_0 (root) ... c_n (= commit)`, fold a **version
table** `VT: version -> count`:

```
VT(c_0) = {}
for each c_i (oldest first):
    V  = version(c_i)                  // main recipe of c_i, "unknown" if none
    rn = releaseStart(c_i)             // recipe Release: leading digits rule, 0 if none
    n  = max(VT[V] + 1, rn)            // VT[V] = 0 when absent
    if V != version(c_{i-1}) and (V == "unknown" or version(c_{i-1}) == "unknown"):
        n = max(n, VT[version(c_{i-1})] + 1)
    VT[V] = n
git_vrev(c_n) = VT(c_n)[version(c_n)]
```

Why first-parent:
- OBS revisions correspond to branch updates (one per scmsync trigger); a PR
  merge is one branch update. Counting the second-parent side would count PR
  commits OBS never saw (kernel-source: 819 commits, 207 first-parent).
- The fold needs a total order; merging version tables of two parents has no
  meaningful definition (max under-counts, sum double-counts).
- `git log --first-parent --diff-merges=first-parent` gives the recipe changes
  of each step relative to the previous step, which is what the fold needs.

Why "all same-version first-parent ancestors" instead of "commits since the last
version change": it is OBS's semantics (revert of a version bump continues the
counter instead of restarting at 1, so the release never goes down for a
version), and the cost of walking the whole first-parent history is small (§4).

### 3.3 B_CNT per versrel (OBS `nextbcnt`)

- `versrel = "<unit recipe Version or unknown>-<vrev>"`, the version of the unit's
  own recipe (flavor spec for flavors, `u.info.Version`).
- `B_CNT = 1 + max(bcnt)` over `build_counters` rows of `(project, repo, arch,
  package, flavor, versrel)`; idempotent per attempt as today (the existing
  full-key lookup returns the stored row). bsgo keeps counting job creations
  (attempts), not finished builds: gaps are harmless, monotonic either way.
- **Legacy floor** (existing rows, `versrel IS NULL`): let `L` be the max legacy
  `ci_cnt` of the unit (project, package, flavor). If the new CI is `< L` it is
  **raised to `L`** (the versrel is renamed accordingly), and if it equals `L`,
  `B_CNT = max(B_CNT, 1 + max legacy bcnt of (project, repo, arch, package,
  flavor) with ci_cnt = L)`. Every new `(CI, B)` is therefore lexicographically
  greater than every installed `160000.1.<n>` / `160000.2.<n>`. No rewrite of
  legacy rows (except a legacy row of the very same job key, see §5).

### 3.4 Upgrade policy

The release must not go down for the same version. Cases:

| Case | Result |
|---|---|
| Linear progress (pin moves to a descendant) | git_vrev grows by the number of first-parent commits; floor never binds |
| Pin skips commits | same, numbers jump |
| Force-push / history rewrite / branch switch | git_vrev may drop; floor + 1 keeps it monotonic |
| Re-pin to an older commit A after B (A→B→A) | new revision, vrev = floor + 1 > vrev(B) (OBS: a revert is a new revision with a higher vrev) |
| Revert of a version bump (1.0→2.0→1.0) | VT keeps the 1.0 count: continues from it (OBS) |
| Version goes down | counter of the lower version (fresh or continued); EVR order is the packager's problem (Epoch), as in OBS |
| Merge commits | first parent only |
| Lagging checker pass still on the old pin | uses the old revision's vrev (looked up by commit); harmless |
| Existing bsgo builds `1.<n>` | new CI ≥ 1; if equal, legacy floor on B_CNT (3.3) |
| OBS-built installations (`41.1`) | not guaranteed: git gives 25 for kernel-source. Optional seed from OBS (W6), see open question 1 |

### 3.5 Never block passes, never mark units broken; on-demand priority

- The checker only reads the revision table (one indexed query per job). A unit
  whose revision is not resolved yet gets the verdict **blocked** with details
  `waiting for release number (source history of <commit[:12]>)`, plus the last
  error if any, and `ps.noteRetry(15s)`. The unit's existing binaries stay in
  the index; other units are unaffected.
- The check happens at the start of `newJob`, before the build-environment
  expansion, so only units that would get a job wait.
- Resolution runs in a background resolver (W3) outside the pass. A resolver
  failure (git host down, commit gone, rate limit) only delays the revision; it
  never changes a unit to broken or failed.
- **On-demand priority.** The resolver works in two tiers: first the revisions
  units wait for *right now*, then the backfill of everything else. The checker
  records the wanted revisions when it returns the blocked verdict above
  (`Store.WantRevisions`, one batched write per pass, `package_revisions.wanted_at`,
  refreshed at most once a minute per revision). `ClaimRevisions` orders by
  "wanted within the last 30 minutes" first, then by age. The checker repeats the
  request on every pass while the unit waits, so a want expires by itself when
  the unit no longer waits (pin moved on, package disabled). A unit that needs a
  job therefore waits seconds, not the duration of the cold-start backfill.
  Priority is about **order**, not parallelism.
- **Polite to the git host.** The default is 2 workers (`--vrev-workers`) and a
  pause of 1 s after every resolution (`--vrev-delay`), with no burst at cold
  start. Errors back off exponentially per revision (1m, 2m, 4m, ... max 30m); an
  error that looks like the server pushing back (429, 503, "too many requests",
  "rate limit") also pauses every worker of the process for 1 minute and retries
  that revision no earlier than after 5 minutes (only git's own text "The requested
  URL returned error: 429/503" counts). After 3 consecutive failures of any kind
  (host down, TLS, challenge page) every worker pauses 1 minute, doubling up to 30
  minutes while failures continue; one success resets it. The cold-start backfill of
  ~3,500 packages therefore takes 30-60 minutes; that is accepted because the
  units that need a job are resolved first.

### 3.6 Multi-replica correctness

- Revision append: inside the `UpsertPackage` transaction with
  `pg_advisory_xact_lock(hashtext('pkgrev/'||project||'/'||package))`; seq is
  `max(seq)+1`; unique `(project, package, seq)`.
- Resolution claim: `UPDATE ... SET claimed_by=$me, claimed_until=now()+lease
  WHERE <lowest pending seq of the package> AND (claimed_until IS NULL OR
  claimed_until < now()) RETURNING`, picking candidates `FOR UPDATE SKIP LOCKED`
  ordered wanted first (3.5) — no coord lease needed. The lease is 2 minutes, renewed
  every 40 s while a resolution runs; a process releases its claims on graceful
  shutdown, a crashed one frees them by lease expiry. The git computation
  is a pure function of `(url, commit, lookup, config)`, so a duplicate after a
  lease expiry yields the same result.
- Finalization (floor, vrev write) in one short transaction under the same
  advisory lock, only for the lowest unresolved seq of the package. Revisions
  are finalized in seq order; a non-latest revision that fails (commit gone from
  the server) is marked `skipped` after 3 attempts and excluded from floors,
  so it cannot block later revisions. The latest revision keeps retrying with
  backoff (1m, 2m, 4m, ... max 30m).

### 3.7 Caching

- `source_history` (Postgres) memoizes the fold result per
  `(url, commit, lookup, algo_rev, config_hash)`: `version`, `release_start`, `vt` (JSONB,
  version→count, keys sorted by `encoding/json`), `main_recipe`, `config_hash`,
  `walked`, `computed_at`. Only requested commits (pins) are stored, never every
  walked commit (3,500 packages × a few pins × ≤ 4 KB).
- Incremental computation: after the history fetch, look up memo rows for the
  commits of the first-parent list (`commit = ANY($2)`, chunks of 1000); the
  nearest one is the base: `VT(base)` from the memo, fold only the commits after
  it. In the normal case (pin moves forward) only the new commits' recipe blobs
  are fetched.
- `algo_rev` is a constant in the code; bumping it ignores old memo rows (like
  `selectionRev` in `pkg/recipe/fetch`). It never changes resolved vrevs.
- S3 is not used: rows are small and the nearest-ancestor lookup needs a query.

## 4. Measurements (src.opensuse.org pool repos, sha256, from this host)

| Repo (branch slfo-1.2) | commits all / first-parent | `--bare --filter=blob:none` full history | spec blobs on first-parent |
|---|---|---|---|
| kernel-source-azure | 4 / 4 | 104 KB, 0.2 s | 4 |
| kernel-source | 819 / 207 | 1.2 MB, 0.4 s | 194 changes, 366 distinct blobs in all history |
| glibc | 1041 / 301 | 1.1 MB | 287 changes |
| systemd | 1668 / 273 | 1.4 MB | 244 changes; 242 blobs fetched in **one** request: 0.31 s, +0.2 MB |

- Explicit blob fetch must be promisor-style:
  `git -c fetch.negotiationAlgorithm=noop fetch <remote> --no-tags
  --no-write-fetch-head --recurse-submodules=no --filter=blob:none --stdin`
  (oids on stdin). A plain `git fetch origin <blob-oid>` fails with "did not
  send all necessary objects". Lazy per-object fetches (what `cat-file` does in a
  partial clone) cost ~0.22 s per blob (94 blobs: 21 s): set
  `GIT_NO_LAZY_FETCH=1` (git ≥ 2.44) on every read so a missing blob is an error,
  never a silent round trip. aisrv has git 2.56.
- `git log --first-parent --diff-merges=first-parent --raw --no-abbrev
  --no-renames -- ':(glob)*.spec'` over glibc's 301 commits: < 10 ms.
- Expected cost per package, cold: 2 network round trips + parse ≈ 1 s.
  3,500 packages with 4 workers would be ≈ 15 min; the implemented default is
  2 workers with a 1 s pause (3.5), so ≈ 30-60 min; incremental: 1 history fetch
  + a few blobs. Gitea's proof-of-work challenge sits in front of the web/API endpoints;
  only the git smart-HTTP protocol is used (as `pkg/recipe/fetch` does).
- No persistent mirrors: a throwaway bare repo per resolution (full blob:none
  history is ≤ ~2 MB for typical repos; kernel-source with all spec blobs of the
  whole history 14 MB, transient). LFS: blob:none never touches LFS content;
  only the recipe blobs (pointers are never recipes) are read.
- Real numbers for the motivating case (raw `Version:` lines; bsgo parser
  expected identical, the values are literal):
  - kernel-source @ `3adc0dd8c228`: Version 6.12.0 on the 25 newest
    first-parent commits (from `59a3535` "Sync changes to SLFO-1.2 branch",
    2025-08-20), older history 2.6.x-3.8.2. All-ancestor count of 6.12.0 is also
    25. Release `<RELEASE>.g...` → start 0. **git_vrev = 25** (OBS: 41).
  - kernel-source-azure @ `b20dbdf296c7`: 4 commits, all 6.12.0, **git_vrev =
    4** (OBS: 2).
  - Both differ from OBS (the OBS revision log of SUSE:SLFO:1.2 is not the git
    first-parent history: syncs squash or split differently, the OBS package may
    predate the git branch or start later, copied revisions). The collision is
    fixed either way: `160000.25.x` vs `160000.4.x`.

## 5. Data model (migration `00016_release_counters.sql`)

```sql
CREATE TABLE package_revisions (
    project     varchar(128) NOT NULL,
    package     varchar(256) NOT NULL,
    seq         integer      NOT NULL,          -- 1, 2, ... per (project, package)
    git_url     text         NOT NULL,
    commit_sha  varchar(64)  NOT NULL,
    state       varchar(16)  NOT NULL DEFAULT 'pending', -- pending | resolved | skipped
    version     text,                          -- main recipe version, 'unknown' allowed
    git_vrev    integer,
    vrev        integer,                       -- final CI_CNT; NULL until resolved
    min_vrev    integer,                       -- lower bound for the next resolution (admin recompute)
    source      varchar(16),                   -- git | admin
    attempts    integer      NOT NULL DEFAULT 0,
    last_error  text,
    next_try_at timestamptz,
    claimed_by  text,
    claimed_until timestamptz,
    wanted_at   timestamptz,                   -- last time a unit waited for it (3.5)
    created_at  timestamptz  NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    PRIMARY KEY (project, package, seq)
);
CREATE INDEX idx_package_revisions_commit ON package_revisions(project, package, commit_sha, seq DESC);
CREATE INDEX idx_package_revisions_pending ON package_revisions(state, next_try_at) WHERE state = 'pending';

CREATE TABLE source_history (
    url          text        NOT NULL,
    commit_sha   varchar(64) NOT NULL,
    lookup       text        NOT NULL,   -- package name, or pinned source.recipe
    algo_rev     integer     NOT NULL,
    config_hash  text        NOT NULL,   -- part of the key: another prjconf is another memo
    main_recipe  text        NOT NULL,   -- '' when none
    version      text        NOT NULL,
    release_start integer    NOT NULL,
    vt           jsonb       NOT NULL,
    walked       integer     NOT NULL,   -- first-parent commits folded in this run
    computed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (url, commit_sha, lookup, algo_rev, config_hash)
);

ALTER TABLE build_counters ADD COLUMN IF NOT EXISTS versrel text;   -- NULL = legacy row
CREATE INDEX IF NOT EXISTS idx_build_counters_versrel
    ON build_counters(project, repo, arch, package, flavor, versrel);
ALTER TABLE package_build_state ADD COLUMN IF NOT EXISTS versrel text NOT NULL DEFAULT '';

-- seed one pending revision per existing package pin
INSERT INTO package_revisions (project, package, seq, git_url, commit_sha)
SELECT project_name, package_name, 1, git_url, commit_sha FROM project_packages
WHERE git_url <> '' AND commit_sha <> '' ON CONFLICT DO NOTHING;
```

Down migration drops the two tables and the columns/index.

A legacy `build_counters` row (no `versrel`) with the same `(commit, meta)` key as a
new allocation belongs to a job that was never built (otherwise no new job is
made for that key; for example a `scheduled` job that is resent after a restart):
it is renumbered in place instead of reusing the old `1.<n>`.

## 6. Work packages

### W1. Shared git plumbing — required, small

Files: new `pkg/recipe/internal/gitrepo` (moved from `pkg/recipe/fetch/fetch.go`:
`repo`, `env`, `exec`, `init`, `ObjectFormat`, `fetchReachable`, `verify`,
`nextBlob`, `parseTreeEntry`), `pkg/recipe/fetch` uses it.

- Keep the safety model: URL and commit never on the command line (remote via
  `GIT_CONFIG_*` env), protocol allow-list, `GIT_LFS_SKIP_SMUDGE=1`,
  `GIT_TERMINAL_PROMPT=0`; add `GIT_NO_LAZY_FETCH=1` for history reads.
- Add `FetchHistory(ctx)` (`git fetch --quiet --filter=blob:none --no-tags src`,
  no depth; fallback `fetchReachable` exactly as today), `FirstParent(ctx, max)
  ([]string, error)`, `FetchBlobs(ctx, oids []string)` (promisor-style command of
  §4, oids validated hex, on stdin, chunks of 256), `ReadBlobs(ctx, oids)`
  (`cat-file --batch`, size limit 16 MiB), `RecipeChanges(ctx, base, tip)`
  (raw first-parent log, see W2).

Acceptance: `pkg/recipe/fetch` tests unchanged and green; new table-driven tests
against synthetic repos in `t.TempDir()` served by `file://` URL, both
`--object-format=sha1` and `sha256`: first-parent list with merges, blob batch
fetch in a partial clone (`uploadpack.allowFilter=true`,
`uploadpack.allowAnySHA1InWant=true` on the source repo), missing blob is an
error (no lazy fetch).

### W2. Version history engine — required

Files: new `pkg/recipe/history` (deep module, no DB, no S3).

```go
type Request struct {
    URL, Commit string
    Lookup      string             // package name (OBS packid)
    Pinned      string             // manifest source.recipe, may be ""
    Config      *buildconfig.Config // project's own prjconf, arch "noarch"
    ConfigHash  string
}
type Point struct { // memo of one commit
    Commit, MainRecipe, Version string
    ReleaseStart int
    VT          map[string]int
}
type Memo interface { // implemented by the store adapter (W3) and an in-memory map (tests)
    Nearest(ctx context.Context, url, lookup string, commits []string) (*Point, error)
}
type Result struct {
    Point
    GitVrev int
    Walked, BlobsFetched int
}
func Resolve(ctx context.Context, req Request, memo Memo) (*Result, error)
```

Algorithm:
1. Temp bare repo (`os.MkdirTemp`, removed on return); `FetchHistory`.
2. `FirstParent(max=10000)` (newest first). If longer, fold only the newest
   10000 and log a warning (treat older history as absent).
3. `memo.Nearest` over the list → base (excluded) or none (fold from the
   oldest listed commit).
4. Recipe file set per step: `git ls-tree -z --long <first commit to fold>`
   (top-level blobs only) then `git log --first-parent --diff-merges=first-parent
   --format=%x00%H --raw -z --no-abbrev --no-renames <tip> [^<base>] --
   ':(glob)*.spec' ':(glob)*.dsc' ':(glob)*.kiwi' ':(glob)Dockerfile*'
   ':(glob)*.productcompose'` to update the set at the commits that change it;
   other commits inherit. Per commit: main recipe = `Pinned` if present in the
   set, else `recipe.FindRecipe(Lookup, "", ext, names)` for `spec, dsc, kiwi`,
   then docker (`Dockerfile.<Lookup>`, `Dockerfile`), then productcompose (port
   the rest of OBS `findfile` only as far as `FindRecipe` lacks it).
5. Distinct main-recipe blob oids not yet present → `FetchBlobs` once, then
   `ReadBlobs`. Parse each distinct blob once:
   `recipe.Parse(cfg, file, content, recipe.Options{Package: Lookup})` without
   `%include` resolution; version = `Info.Version`, `unknown` when empty,
   `MultiVersion`, parse error or `Info.Error`; release start from `Info.Release`
   with the getcommitinfo rule (§2.1).
6. Fold VT (§3.2) from base to tip; return the tip's Point and GitVrev.

Errors: fetch errors wrapped with url/commit (same text style as the recipe
fetcher); context cancellation propagates; no partial results.

Acceptance (table-driven, synthetic repos in `t.TempDir()`, sha1 and sha256):
linear same version (n commits → n); version bump resets to 1; revert of a bump
continues (1.0×3, 2.0×2, 1.0 → 4); commits not touching the spec count; merge
commit counts once and its second-parent commits not at all; spec renamed /
`Pinned` recipe; no recipe → `unknown`; known→unknown→known rule; `Release: 7`
start value; `#!ForceMultiVersion` → unknown; multibuild repo uses the main
spec, not flavor specs; memo base in the middle gives the same result as a full
fold; blob fetched once per distinct oid (count via `BlobsFetched`);
cap of the walk; kernel-source-like shape (old versions, long same-version run).

### W3. Storage and resolver — required

Files: `pkg/storage/postgres/migrations/00016_release_counters.sql`,
`pkg/storage` (types, `Store` methods, `MemoryStore`),
`pkg/storage/postgres` (methods, `UpsertPackage`),
new `pkg/scheduler/vrev` (resolver), `cmd/bsgo-server` (wiring, flags).

Store methods (names indicative):
- `UpsertPackage`: in one transaction with the advisory lock of §3.6, read the
  latest revision; if `(git_url, commit_sha)` differs (or none), insert
  `seq+1` pending. Same in `MemoryStore`.
- `RevisionFor(ctx, project, pkg, commit) (*PackageRevision, error)`: latest seq
  with that commit (`ErrNotFound` if none).
- `ClaimRevisions(ctx, owner, limit, lease) ([]PackageRevision, error)`: per
  package only the lowest pending seq, `next_try_at <= now()`.
- `FinishRevision(ctx, rev, result)`: under the advisory lock compute floor and
  the unknown rule from resolved revisions with lower seq, write vrev, state
  `resolved`; `FailRevision(ctx, rev, err)`: attempts+1, backoff, `skipped` per
  §3.6.
- `NearestHistory` / `PutHistory` for `source_history` (`ON CONFLICT DO
  NOTHING`, then re-read).
- `WantRevisions(ctx, refs)`: marks the pending revisions of the given
  `(project, package, commit)` as wanted (3.5); `ClaimRevisions` serves wanted ones
  first. `ListRevisions`, `RevisionCounts`, `ResetRevisions` and `SetRevisionVrev`
  serve the CLI (W5) and the progress line.

Resolver (`pkg/scheduler/vrev`):
- Loop every 10 s (and immediately after start): claim up to `workers` rows,
  resolve with `history.Resolve`, finish or fail. Config: the project's own
  prjconf via `buildconfig.Render(prj, "", "", project)` parsed with arch
  `noarch`; `ConfigHash` = sha256 of that text.
- Server flags: `--vrev-workers` (default 2; 0 disables; be polite to the git
  host, priority is order, not parallelism), `--vrev-delay` (pause of a worker
  after each resolution, default 1s), `--vrev-timeout` (per resolution, default
  5m). Workers claim one revision at a time, so a newly wanted revision is
  served by the next free worker.
- Logs, one line per resolution, no decoration:
  `[VREV] SUSE:SLFO:1.2/kernel-source 3adc0dd8c228: version 6.12.0 vrev 25 (git 25) walked 207 blobs 194 in 1.2s`
  `[VREV] SUSE:SLFO:1.2/foo 0123456789ab: failed (attempt 2, retry in 2m): <error>`
  and a summary every 5 minutes while rows are pending:
  `[VREV] pending 1234, resolved 2266, skipped 3`.

Acceptance: MemoryStore and Postgres (integration test against the podman
compose Postgres) table tests: append on commit change only; url change appends;
`RevisionFor` returns the latest seq of a commit; claim gives one row per package
and respects leases; floor cases of §3.4 (force-push lower git count,
A→B→A, revert of bump, unknown rule); skipped non-latest revision does not block;
idempotent `PutHistory` across two concurrent writers.

### W4. Checker and counters — required

Files: `pkg/scheduler/checker/jobbuild.go`, `checker.go` (Store interface),
`jobspec.go`, `meta.go` (`release` unchanged except input), `pkg/storage`
(`CounterKey`, `BuildCounters` docs), `pkg/storage/postgres/checker_state.go`,
`pkg/storage/memory_checker.go`, optionally `package_build_state.versrel`
(new column in 00016) for the CLI.

- `newJob`: first `RevisionFor(project, u.pkg, u.commit)`; not found or not
  resolved → blocked verdict of §3.5 (`ErrNotFound` additionally logs once per
  pass: it means a manifest written without revision, e.g. a store written by an
  old binary). Store errors keep today's handling (`ps.fail`, broken).
- `versrel := version(u.info.Version) + "-" + strconv.Itoa(rev.Vrev)`.
- `CounterKey` gains `Versrel string` and `CI int` (from the revision);
  `NextBuildCounters` no longer allocates CI: it returns `{CI: key.CI, B: ...}`
  with B per §3.3 incl. the legacy floor, inserts the row with `versrel`.
  The full-key idempotency lookup stays (a retried pass reuses its attempt's
  counters even if the revision was re-resolved meanwhile).
- `JobSpec` gains `Versrel` (logged, shown by `bsgo package why`, recorded in the
  build state row). Release rendering (`release`, `cicntstart` handling) is
  unchanged.

Acceptance (checker tests with MemoryStore): pending revision → blocked with
the text above and a retry, no job; resolved → release `160000.25.1` for a
kernel-like fixture; flavor versrel uses the flavor recipe version; B_CNT
continues for the same versrel across attempts, restarts at 1 for a new versrel;
legacy floor (legacy `ci=1,bcnt=130`, new CI 1 → B 131; new CI 2 → B 1);
retried attempt returns stored counters.

### W5. CLI — implemented

Files: `pkg/cli/admin_release.go` (+ tests), `pkg/cli/admin.go`,
`docs/specs/04-access-control-and-auth.md` (scope `admin:release[:<project>]`).

`bsgo admin release` subcommands (operator CLI with direct store access, like
`admin gc`):
- `show <project> [<package>...] [--all-revisions] [--json]`: per package the
  latest revision (seq, commit, version, git_vrev, vrev, state, last error) and,
  per unit known to the build state, the last versrel/bcnt
  (`package_build_state.versrel`). A named package without a revision log is an
  error.
- `recompute <project> <package>...|--all [--allow-decrease] [--dry-run] [--yes] [--json]`:
  drops the `source_history` rows of the packages' git urls and sets the **latest**
  revision to pending (a revision that is pending already is left alone); on finish
  the new vrev is `max(old vrev, computed)` (`min_vrev`) unless `--allow-decrease`
  (prints a warning on stderr; changes no existing binaries). The unit waits as
  blocked until the resolver is through (seconds: the checker records it as wanted).
- `set <project> <package> --vrev N [--commit C] [--version V] [--dry-run] [--yes] [--json]`:
  sets the latest revision (must be pinned to `--commit`, a prefix is enough, if
  given) to `vrev = N`, `source = admin`; `--version` is needed only while the
  revision has no version yet.
- `seed <project> --file F ...`: not implemented, see W6.

Rich `--help` with examples; without `--yes`/`--dry-run` mutations ask when
interactive and fail when not; `--json` prints one deterministic document on
stdout, diagnostics (warnings) go to stderr, no ANSI.

### W6. Optional seed from OBS — not now (decision 1)

Purpose: release continuity with OBS-built installations (bsgo kernel-source
would be `160000.25.x` while OBS shipped `160000.41.1`).
- Input file (CSV, header `package,commit,version,vrev`), produced by the user
  with `iosc` (bsgo has no OBS credentials): per package the OBS vrev of the
  revision that corresponds to the current pin. Helper script
  `hack/obs-vrev-export.sh` using `iosc api
  '/source/SUSE:SLFO:1.2/<pkg>?view=info'` (fields `vrev`, `version`, and the
  scmsync commit from `_scmsync.obsinfo`) — verify the exact fields when writing
  it.
- `seed`: if the package's latest revision has that commit: set its vrev to
  `max(vrev, seed)` (`source = seed`); otherwise append nothing and report the
  mismatch. Later revisions then get `max(git_vrev, seed + 1)` through the floor.
- B_CNT continuity with OBS is not seeded (OBS bcnt is per prpa history; the
  legacy floor and the CI jump make it unnecessary unless CI is equal).

### W7. Validation on aisrv — operator step after deploy

1. After deploy, wait for `[VREV] pending 0` (expected 30-60 min with the default 2 workers; units that need a job are resolved first and wait seconds).
2. `bsgo admin release show SUSE:SLFO:1.2 kernel-source kernel-source-azure --json`:
   expect vrev 25 and 4 (§4).
3. Sample of ~30 packages (mix: small, kernel, multibuild, packages with merges,
   `unknown` versions): compare with `iosc ls -b SUSE:SLFO:1.2 <pkg> -r standard
   -a x86_64` (release `160000.<vrev>.<bcnt>`) and `iosc log SUSE:SLFO:1.2 <pkg>`
   (revision list). Classify differences: (a) OBS revisions ≠ first-parent
   commits (squashed/split syncs, OBS package older or younger than the git
   branch), (b) copied revisions, (c) version macro differences (must be zero:
   compare the version too). Record the table here.
4. Uniqueness check: no two units of a prpa produce the same NEVRA with
   different content after the next rebuild (publisher conflict list empty for
   the kernel case).
5. Monotonicity check: for every unit, the next build's release is greater
   (rpmvercmp) than the release currently in the index.
6. Force rebuild of `kernel-source` and `kernel-source-azure` (the collision is
   fixed only for new builds): `bsgo admin trigger SUSE:SLFO:1.2 kernel-source
   --force`, same for `kernel-source-azure`.

### W8. Docs

`docs/guides/solv-index-explained.md` (or the scheduler guide): release
numbers, revision log, resolver flags, CLI, OBS differences. Glossary terms:
*package revision*, *vrev*, *versrel*.

## 7. Alternatives considered

| Alternative | Verdict |
|---|---|
| Keep today's counter, only reset on version change | Still starts at 1 for every package on first sight: same collisions as today; numbers unrelated to history |
| "Commits since the last version change" (no full walk) | Cheaper by a few blobs, but a revert of a version bump restarts at 1 → release goes down for that version; full first-parent walk costs < 1 s |
| All ancestors instead of first-parent | Counts PR-internal commits OBS never sees; fold over a DAG ill-defined |
| Pure git_vrev without revision log | Deterministic but not monotonic on force-push, branch switch, re-pin to older commits |
| Import vrev from OBS once (only) | Exact parity for the current pin, but needs OBS access and gives nothing for new projects; kept as optional seed (W6) on top of the git rule |
| Hash-based counters | Not monotonic, release ordering meaningless |
| Uniqueness check of binary names in the indexer | Does not fix numbering; two packages with equal version and equal vrev can still collide (OBS has the same property). Publisher already reports `NameConflict`; follow-up: also warn in the indexer when two units of a prpa provide the same name-EVR-arch with different checksums (plain log line, `bsgo project status` field) |
| Persistent bare mirror per package | ~1-2 MB each (≈ 5 GB for 3,500 with growth) and state to manage; throwaway blob:none clones cost < 0.5 s; not needed |
| Depth doubling / `--shallow-since` | Saves < 1 MB per fetch but needs extra round trips and cannot tell when a version reappears; not needed |

## 8. Out of scope

- `cicntstart` exact OBS semantics (prefix like `42.1` kept in vrev): bsgo keeps
  its integer offset in `release`; SLFO does not set it. Follow-up.
- `bcntsynctag` / `relsyncmax`.
- Rebuilding existing binaries automatically (operator decides, W7.6).
- Non-git sources (bsgo has none).

## 9. Order

W1 → W2 → W3 → W4 → W5 → W8 → deploy → W7. W6 only if open question 1 is
answered "yes", after W5. W2 and the storage part of W3 are independent once W1
exists.

## 10. Open questions for the user

1. Must bsgo builds upgrade OBS-built SLFO 1.2 installations (release ≥ OBS's)?
   If yes, W6 seeding is required; git alone gives smaller numbers for packages
   like kernel-source (25 vs 41).
2. Is git first-parent count acceptable as the baseline even though it matches
   OBS only by coincidence (kernel-source 25 vs 41, kernel-source-azure 4 vs 2)?
3. Re-pin to an older commit gets a new, higher vrev (OBS semantics) instead of
   reusing the old one: confirm.
4. Units wait as `blocked` until their revision is resolved (seconds for units that
   need a job, the resolver serves them first; the full backfill takes 30-60 min
   after the first deploy): acceptable?
5. Both `kernel-source:kernel-azure` and `kernel-source-azure:kernel-azure` are
   built (as in OBS). Should one be excluded in the prjconf
   (`BuildFlags: excludebuild:kernel-source:kernel-azure`) to avoid two
   different `kernel-azure` packages in one repository?

### Decisions (user, 2026-11-26)

1. No: bsgo builds need not upgrade OBS-built installations. W6 is not
   implemented now.
2. Yes: the git first-parent count is the baseline.
3. Confirmed: re-pinning an older commit gets a new, higher vrev.
4. Accepted: units wait as `blocked` until their revision is resolved.
5. No exclusion: both `kernel-azure` builds stay, as in OBS.
6. On-demand priority: the resolver resolves first the revisions of units that
   need a job right now (recorded by the checker), before the background
   backfill of all other packages, so a unit waiting to build waits seconds, not
   the duration of the cold-start backfill (3.5).
7. Polite to the git host: low default concurrency (2 workers, 1 s pause, no burst
   at cold start), exponential backoff on errors and 429, a slower cold-start
   backfill (30-60 min) is fine (3.5).

### W8 notes (known gaps and operator facts)

- A permanently failing **latest** revision (for example a commit that no longer
  exists on the server) is retried forever with backoff and its units stay blocked;
  the operator unblocks it with `bsgo admin release set <project> <package> --vrev N
  --version V`.
- Only spec recipes are parsed: a package whose main recipe is a kiwi, dsc or
  Dockerfile gets version `unknown` (OBS parses those). Known gap.
- Git hardening of the history fetch: no user or system git config, protocols limited
  to https and http, blob-less fallback fetch.
- The resolver removes `bsgo-history-*` temp directories older than an hour at
  startup.
