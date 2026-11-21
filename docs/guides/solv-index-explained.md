# The solv index explained: what it is, how it is stored, how bsgo extends it

This guide explains the build repository's dependency index ("solv blob"):
where it lives, what the binary file looks like, how bsgo adds its own data to
it, how it is updated and what it is used for. It complements sections 2 to 4 of
[build-repository-walkthrough.md](build-repository-walkthrough.md) and
[meta-trees-explained.md](meta-trees-explained.md).

## 1. One index per project, repository and architecture

There is one index per prpa (project/repository/architecture). On aisrv, table
`build_repo_index` in Postgres:

```
SUSE:SLFO:1.2 | standard | i586   | buildrepo/SUSE:SLFO:1.2/standard/i586/f7904021...b658d.solv
SUSE:SLFO:1.2 | standard | x86_64 | buildrepo/SUSE:SLFO:1.2/standard/x86_64/0c5b2acc...60d5e.solv
```

The SLFO 1.1 bootstrap repository (a path dependency, not built by the project)
has its own index, cached in table `target_solv_cache`.

The checker loads the project's index first, then the bootstrap index. A binary
name found in the project index hides the same name from 1.1: that is how a
freshly built 1.2 `glibc-devel` replaces the 1.1 one.

## 2. Storage

- The index is **libsolv's binary format**, the same as OBS's `:full.solv`, not
  text. Strings are stored once in a pool, so loading thousands of RPMs with all
  provides and requires takes milliseconds.
- It is stored in **S3** (Garage), not in Postgres. The object key contains the
  sha256 of the content (content-addressed), so a version is never overwritten.
  Next to it is `<hash>.entries.json`: the same set of binaries as identity only
  (unit, S3 key, name, evr, sha256).
- Postgres only holds the **pointer** to the current version
  (`build_repo_index.solv_key`). Switching to a new index is one row update, so
  readers always see a complete, consistent index.
- `dumpsolv` (package libsolv-tools) prints an index as text.

## 3. A real 2-RPM index

Generated with bsgo's own code (`solv.BuildUnitSolv`): source package `zlib`
builds `libz1`, source package `hello` builds `hello`, which requires
`libz.so.1()(64bit)`. The whole file is 429 bytes.

Raw bytes (`xxd`), excerpt:

```
00000000: 534f 4c56 0000 0008 ...                  SOLV....            magic "SOLV", format version 8
00000030: 6570 6f6b 6579 3a74 7970 653a 6964 00    epokey:type:id.     string pool starts
00000040: 312e 332e 312d 312e 3100 0032 2e31 322d  1.3.1-1.1..2.12-
00000050: 332e 3100 0068 656c 6c6f 0000 6c69 627a  3.1..hello..libz
00000080: ...       7838 365f                      ...x86_
00000090: 3634 0000 6269 6e00 0173 676f 3a75 6e69  64..bin..sgo:uni    "bsgo:unit" stored as "b" + "sgo:unit"
000000a0: 7400 ...                                 t..
...
00000180: 0768 656c 6c6f 0000 ...                  .hello..            bsgo:unit value of solvable 1
00000190: 167a 6c69 6200 ...                       .zlib.              bsgo:unit value of solvable 2
```

The same file as text (`dumpsolv`):

```
repodata 1 has 9 keys, 3 schemata
  solvable:name        (type repokey:type:id)
  solvable:arch        (type repokey:type:id)
  solvable:evr         (type repokey:type:id)
  solvable:provides    (type repokey:type:idarray)
  solvable:requires    (type repokey:type:idarray)
  repository:solvables (type repokey:type:flexarray)
  bsgo:unit            (type repokey:type:str)          custom key added by bsgo
  solvable:filelist    (type repokey:type:dirstrarray)
  solvable:checksum    (type repokey:type:sha256)

solvable 1 (2):
solvable:name: hello
solvable:evr: 2.12-3.1
solvable:provides:
  hello = 2.12-3.1
solvable:requires:
  libz.so.1()(64bit)
bsgo:unit: hello
solvable:filelist:
  /usr/bin/hello
solvable:checksum: 2222...2222 (sha256)

solvable 2 (3):
solvable:name: libz1
solvable:evr: 1.3.1-1.1
solvable:provides:
  libz.so.1()(64bit)
  libz1 = 1.3.1-1.1
bsgo:unit: zlib
solvable:checksum: 1111...1111 (sha256)
```

## 4. How the custom data is added

bsgo adds two pieces of data to every binary, through libsolv's C API in
`pkg/solv/c_bridge.c`:

- **sha256 of the RPM**: libsolv's standard checksum field,
  `repodata_set_checksum(data, sid, SOLVABLE_CHECKSUM, REPOKEY_TYPE_SHA256, hex)`.
- **`bsgo:unit`**, the source package (unit) that built the binary
  (`glibc-devel` -> `glibc`, `cpp-build` -> `gcc:build`). libsolv allows keys of
  your own: bsgo registers the name with `pool_str2id(pool, "bsgo:unit", 1)` and
  stores a string per binary with `repodata_set_str(...)`; it reads it back with
  `solvable_lookup_str(...)`.

`bsgo:unit` is **not a hard-coded id** in the format. libsolv writes every key
by name, exactly like its own `solvable:name`:

1. **String pool**: key names (`solvable:name`, `bsgo:unit`) and values
   (`hello`, `x86_64`) are strings in the file's pool. The pool is
   prefix-compressed: `bsgo:unit` follows `bin`, so it is stored as "share one
   character (`b`), then `sgo:unit`" (`01 73 67 6f 3a 75 6e 69 74` in the hex).
2. **Key table**: each key is name, type, size and storage; the name is a
   reference into the string pool. These numbers are assigned per file when it is
   written; nothing is reserved for `bsgo:unit`.
3. **Data**: per binary, the values in the order of its schema. `bsgo:unit` has
   type `str`, so the value (`hello`, `zlib`) is stored inline.

Tools that do not know the key (zypper, plain libsolv programs) read the file
normally and ignore it.

OBS does not need such a key: it keeps each package's binaries in its own
directory on disk (`<prpa>/<package>/`) and rebuilds `:full.solv` from those
files. bsgo has no shared filesystem, so "which package built this RPM" travels
inside the index.

## 5. Delta update after each build

`solv.UpdateSolv` (`pkg/solv/index.go`), called by the indexer when a package
finishes:

1. load the current index;
2. remove all binaries whose `bsgo:unit` is that package
   (`bsgo_repo_remove_unit`);
3. add its new binaries from the job's `bininfo.json` (name, version, provides,
   requires, files, sha256);
4. write the result as a new content-addressed object in S3, then switch the
   pointer in `build_repo_index`.

A build only touches its own binaries; the index is never rebuilt from all RPMs.
Without `bsgo:unit`, step 2 would not know which entries to drop.

## 6. What the index is used for

Every checker pass loads it and expands each package's build dependencies
(BuildRequires from the spec plus the prjconf's `Required:` packages) against it,
like obs-build. That expansion decides:

- **unresolvable** ("nothing provides X") or **blocked** (a needed binary's
  producer is still being built);
- **the job's exact RPM list**: every RPM with its sha256 and S3 location; the
  worker downloads exactly those and verifies them;
- **the dependency graph and cycles**: build order, the cycle rule, job priority
  (number of dependents);
- **the meta tree**: for each binary used, its producer (`bsgo:unit`), so the
  producer's built commit and stored tree can be nested
  ([meta-trees-explained.md](meta-trees-explained.md)).

A rebuild "ripples" at the next checker pass after the indexer has swapped in
the new index: the expansion finds the new binaries and the trees change.

## 7. Space: garbage collection

Every finished build writes a new `.solv` (about 1.1 MB for SLFO 1.2 x86_64) and
`.entries.json` (about 1.3 MB). Writing new objects is what makes updates safe
(no in-place modification, readers always see a complete version, one-row
switch), and builds never delete anything themselves. Before garbage collection
existed, aisrv held on 2026-11-21 10,240 index objects (6.7 GiB) and 46.8 GiB of
artifacts, including every old RPM release of every unit.

`pkg/gc` removes what no current pointer can reach. The rules:

- **Grace period** (default 24h): nothing younger is ever deleted, so a checker
  pass, a publisher run or a job that loaded a pointer a moment ago never finds
  an object missing. Jobs that are queued or running resolved their build
  dependencies from the index when they were scheduled; the grace period is what
  protects them, the dispatcher is not consulted.
- **`buildrepo/`**: the pair referenced by the `build_repo_index` row of a prpa
  is kept, plus the last N superseded pairs of that prpa by modification time
  (default 3, for debugging). `.solv` and `.entries.json` of a version are
  deleted together. A prpa without a row (removed project, repository or
  architecture) is garbage after the grace period.
- **`artifacts/`**: a file in `artifacts/<project>/<repo>/<arch>/<unit>/` is
  kept when the current `entries.json` of any prpa lists its key (this covers
  binaries exported into another architecture's repository, whose object lives
  in the exporting unit's directory), when it belongs to the current build of
  the unit (listed in the unit's current `bininfo.json`, which includes
  debuginfo, debugsource and source RPMs that are not in the index), or when it
  is a metadata file (`bininfo.json`, `_slsa_provenance.json`, `_statistics`,
  `build.log`). Older RPMs of the unit are deleted. Units that no longer exist
  (no referenced binary and no package of that name in the project) lose all
  files older than the grace period. A unit that has no `bininfo.json` is kept
  as a whole.
- Never touched: `repos/` (owned by the publisher), `backup/`, `recipes/`, `dod/`.
- A run refuses to start when the pointer table is empty while objects exist, so
  a server that fell back to an empty store cannot declare everything garbage.
- Pointers are read again right before each batch of deletions
  (S3 `DeleteObjects`, up to 1000 keys), so a pointer swap during a run never
  loses the new current objects.
- Only one run happens at a time: the `gc` lease in NATS KV, shared by all server
  replicas and the CLI. Errors of single prpas are logged and the run continues.

The server runs it in the background; one plain log line per prefix:

```
[GC] buildrepo: kept 6, deleted 10234 objects, freed 6.6 GiB in 41s
```

| Server flag | Default | Meaning |
|---|---|---|
| `--gc-interval` | 6h | time between runs, `0` disables |
| `--gc-grace` | 24h | minimum object age before deletion |
| `--gc-keep-index-versions` | 3 | superseded index versions kept per prpa |

By hand, with the CLI (access tokens need the scope `admin:gc`):

```bash
bsgo admin gc --prefix all --dry-run --json > gc-plan.json   # review first
bsgo admin gc --prefix buildrepo --yes
bsgo admin gc --prefix artifacts --project SUSE:SLFO:1.2 --grace 48h --yes
```

Without `--yes` it asks on a terminal and fails otherwise; `--dry-run` never
deletes. With the index kept to the current version plus three, the steady state
is a few MB of index per architecture.

## Code to read

| Topic | bsgo | OBS |
|---|---|---|
| libsolv bridge, custom key | `pkg/solv/c_bridge.c` | `BSSolv.xs` |
| build and delta update of the index | `pkg/solv/index.go` (`BuildUnitSolv`, `UpdateSolv`) | `BSSched/BuildRepo.pm` (`writesolv`) |
| integration of a finished build | `pkg/scheduler/indexer` | `BSSched/BuildJob.pm` (`jobfinished`) |
| path layering, shadowing | `pkg/solv/pathpool.go` | `BSSched/Checker.pm` (`createpool`) |
