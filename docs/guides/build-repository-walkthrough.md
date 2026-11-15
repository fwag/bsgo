# Walkthrough: bininfo, the build repository, full.solv and meta

This guide follows three tiny RPMs through the bsgo build repository and
compares each artifact with its OBS counterpart. All outputs in
[`examples/build-repo-demo/out/`](examples/build-repo-demo/out/) were produced
by the real bsgo code (`pkg/bininfo`, `pkg/scheduler/buildrepo`, `pkg/solv`),
and the solv dump comes from libsolv's `dumpsolv`.

Terms follow [GLOSSARY.md](../../GLOSSARY.md). A *prpa* is one
project/repository/architecture triple, e.g. `SUSE:SLFO:1.2/standard/x86_64`.

## The demo project

Project `home:demo`, target `standard`, arch `x86_64`, two source packages
([specs](examples/build-repo-demo/specs/)):

| Source package (unit) | Binary RPMs | Provides | Requires |
|---|---|---|---|
| `libgreet` | `libgreet1` | `libgreet.so.1()(64bit)` | |
| | `libgreet-devel` | `pkgconfig(greet) = 1.0` | `libgreet1 = 1.0`, `/usr/bin/pkg-config` (added by rpm) |
| `hello` | `hello` | | `libgreet.so.1()(64bit)` |

`hello` also has `BuildRequires: pkgconfig(greet) >= 1.0`.

## Where things live

OBS keeps everything in a directory per prpa on a shared filesystem:

```
/srv/obs/build/<prj>/<repo>/<arch>/
  <package>/      last successful build: *.rpm, logfile, .bininfo, .meta
  :full/          union of the binaries of all packages (what others build against)
  :full.solv      :full in libsolv binary format, with dependencies
```

bsgo has no shared filesystem. The same roles are played by S3 objects and
PostgreSQL rows:

| Role | OBS | bsgo |
|---|---|---|
| Result of one package build | `<package>/` directory | S3 prefix `artifacts/<prj>/<repo>/<arch>/<unit>/` |
| Per-binary metadata | `.bininfo` (Perl Storable) | `bininfo.json` in that prefix |
| Current binary set of the prpa | `:full/` listing | build-repo **entries** (S3, content-addressed) |
| Dependency index | `:full.solv` | build-repo **solv blob** (S3, content-addressed) |
| Which index is current | the files themselves | pointer row in `build_repo_index` (Postgres), swapped atomically |
| What a build was made from | `.meta` file | `package_build_state.meta` (Postgres) |

## 1. bininfo: written once per package build

The worker has the RPMs on local disk right after the build. It extracts the
header data with `rpm -qp --qf` (`pkg/bininfo`) and uploads one
`bininfo.json` next to the RPMs. Abridged from
[`libgreet.bininfo.json`](examples/build-repo-demo/out/libgreet.bininfo.json):

```json
{ "name": "libgreet-devel", "version": "1.0", "release": "1.1", "arch": "x86_64",
  "sourcerpm": "libgreet-1.0-1.1.src.rpm",
  "filename": "libgreet-devel-1.0-1.1.x86_64.rpm",
  "sha256": "1fd814f5ff46ca2925335ddd771bad9352dd7d733256d3a68ca2c0747f43dafe",
  "provides": [ {"name": "libgreet-devel", "op": "=", "evr": "1.0-1.1"},
                {"name": "pkgconfig(greet)", "op": "=", "evr": "1.0"} ],
  "requires": [ {"name": "/usr/bin/pkg-config"},
                {"name": "libgreet1", "op": "=", "evr": "1.0"} ],
  "files": [ "/usr/bin/greet-config" ] }
```

- `files` is the *filtered* file list, the same subset primary.xml carries:
  paths containing `bin/`, paths under `/etc/`, and `/usr/lib/sendmail`.
  `/usr/share/pkgconfig/greet.pc` is not in it.
- Identity is the sha256 of the RPM file, not an MD5.
- **Difference from OBS:** OBS's `.bininfo` holds identity only (filename,
  name, evr, arch, hdrmd5). OBS reads dependencies from RPM headers on its
  local disk when it writes `:full.solv`. bsgo's indexer would have to
  download every RPM from S3 to do the same, so the worker ships the header
  data once in `bininfo.json`.

## 2. The entries index: which binaries are current

[`entries.json`](examples/build-repo-demo/out/entries.json) lists every
binary of the prpa that other packages build against. It holds identity only:

| unit | key (S3) | name | evr | sha256 |
|---|---|---|---|---|
| hello | `artifacts/home:demo/standard/x86_64/hello/hello-2.12-3.1.x86_64.rpm` | hello | 2.12-3.1 | `996a68f3…` |
| libgreet | `…/libgreet/libgreet-devel-1.0-1.1.x86_64.rpm` | libgreet-devel | 1.0-1.1 | `1fd814f5…` |
| libgreet | `…/libgreet/libgreet1-1.0-1.1.x86_64.rpm` | libgreet1 | 1.0-1.1 | `20e0d585…` |

When `libgreet` is rebuilt, its two rows are replaced as a set and `hello`'s
row is untouched. That is OBS's "integrate the package into `:full`".
Source and debug RPMs are never in this index (OBS "useforbuild").

## 3. The solv blob: the same set with dependencies

The scheduler does not parse JSON or XML per pass. It loads the solv blob, a
libsolv binary repository (703 bytes here). From
[`full.solv.dump.txt`](examples/build-repo-demo/out/full.solv.dump.txt):

```
solvable:name: libgreet-devel
solvable:evr: 1.0-1.1
solvable:provides:
  libgreet-devel = 1.0-1.1
  libgreet-devel(x86-64) = 1.0-1.1
  pkgconfig(greet) = 1.0
solvable:requires:
  /usr/bin/pkg-config
  libgreet1 = 1.0
bsgo:unit: libgreet
solvable:filelist:
  /usr/bin/greet-config
solvable:checksum: 1fd814f5… (sha256)
```

- Strings are stored once in a pool, and libsolv builds its "what provides X"
  index on load, so loading is milliseconds even for 15k binaries.
- `bsgo:unit` is a custom key. It lets the indexer drop one unit's
  solvables and add the new ones (`solv.UpdateSolv`) instead of rebuilding
  the whole blob after every build.

## 4. Expansion: what the scheduler does with it

To build `hello`, the scheduler expands its BuildRequires against the path:
the project's own solv blob first, then path dependencies such as the SLFO 1.1
Bootstrap Dependency. A name found earlier in the path hides the same name
later in it. In the demo:

```
BuildRequires: pkgconfig(greet) >= 1.0
  -> libgreet-devel 1.0-1.1   (provides pkgconfig(greet) = 1.0)
  -> libgreet1      1.0-1.1   (required by libgreet-devel)
```

`/usr/bin/pkg-config` was **not** followed. obs-build's expander ignores file
requirements unless the project config maps them with `FileProvides:`
(`Build/Rpmmd.pm`: "we ignore file provides in expanddeps"). That is why the
SLFO prjconf has 342 `FileProvides` lines. bsgo matches that behaviour.

The resulting list is the job's exact dependency set (`BuildInfo.Bdeps`).
The worker downloads exactly these binaries and verifies each sha256, then
passes them to obs-build with `--rpmlist`.

## 5. meta: has anything changed since the last build?

```
meta(hello) = sha256( "bsgo-meta-v1\n"
                      + <hello source commit> + "\n"
                      + sorted bdep sha256s, one per line )
            = 57a2e55ac5f534599505c8a2145fd77658ca09060243dba196a7079157130b62
```

After `hello` succeeds, this value is stored in `package_build_state`. On every
later pass the scheduler recomputes it:

- **Unchanged** (same commit, same dependency binaries): `hello` stays
  `succeeded`. No rebuild.
- **Changed** (e.g. `libgreet` was rebuilt and `libgreet1` now has a
  different sha256): `hello` is scheduled again.

OBS's `.meta` is the same idea as text: `<srcmd5>  hello` followed by one
`<hdrmd5>  <name>` line per dependency, compared line by line
(`BSSched/BuildJob.pm:metacheck`). bsgo uses the git commit and sha256
instead of md5 values.

## End-to-end flow

```
worker builds libgreet
  -> uploads RPMs + bininfo.json to artifacts/<prpa>/libgreet/
  -> job-finished event
indexer
  -> replaces libgreet's rows in entries, UpdateSolv on the solv blob
  -> writes both content-addressed to S3, swaps the build_repo_index pointer
  -> emits repo-changed for the prpa
scheduler pass
  -> loads the solv blob, expands every package
  -> hello's meta changed -> new job with the exact bdeps
publisher
  -> publishes the current entries as an rpm-md repository
```

## Run it yourself

```bash
E=/tmp/bsgo-demo; mkdir -p $E
for s in libgreet hello; do
  rpmbuild -bb --nodeps --define "_topdir $E" --define "debug_package %{nil}" \
    --define "__os_install_post %{nil}" docs/guides/examples/build-repo-demo/specs/$s.spec
done
DEMO_RPMS=$E/RPMS/x86_64 go run docs/guides/examples/build-repo-demo/main.go
dumpsolv docs/guides/examples/build-repo-demo/out/full.solv   # needs libsolv-tools
```

The sha256 values differ on every run because rpmbuild embeds build times.

## Code to read

| Topic | bsgo | OBS |
|---|---|---|
| bininfo extraction | `pkg/bininfo` | `BSSched/BuildJob.pm:jobfinished`, `.bininfo` writing in `bs_repserver` |
| Solv blob, incremental update | `pkg/solv/index.go` (`BuildUnitSolv`, `UpdateSolv`) | `BSSched/BuildRepo.pm` (`fctx_integrate_package_into_full`, `writesolv`) |
| Path layering, shadowing | `pkg/solv/pathpool.go` | `BSSched/Checker.pm:createpool` |
| Expansion | `pkg/solv/expand.go` | `/usr/lib/build/Build/Expand.pm` |
| Integration of a finished build | `pkg/scheduler/indexer` | `BSSched/BuildJob.pm:jobfinished` |
| meta | `pkg/scheduler/checker/meta.go` | `BSSched/BuildJob.pm:metacheck` |
