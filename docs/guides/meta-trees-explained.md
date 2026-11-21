# Meta trees explained: how bsgo decides that a package must be rebuilt

This guide explains, with examples and real data from the SUSE:SLFO:1.2
bootstrap on aisrv, how bsgo records what a package was built from and how it
decides that a rebuild is needed. It complements section 5 of
[build-repository-walkthrough.md](build-repository-walkthrough.md), which
describes the format; this guide explains the behaviour.

Terms: a *source package* (unit) is what is built (`glibc`, `gcc13`,
`gcc:build`); it produces *binary packages* (RPMs: `glibc`, `glibc-devel`,
`glibc-locale-base`, ...). Build dependencies are always RPM names.

## 1. What is stored

| Where | What |
|---|---|
| `package_build_state.meta` | hash of the tree the checker computed in its last pass: what the package *would* be built from now |
| `package_build_state.built_meta` | hash of the tree of the last successful build: what it *was* built from |
| `package_build_state.built_commit` | source commit of the last successful build |
| `build_meta.lines` | the trees themselves, gzip-compressed text, one per distinct meta hash |

The rule: `meta` different from `built_meta` means the package must be rebuilt
("meta change"). A different `source_commit` means "source change".

## 2. What a tree is

A tree is a text file, one line per dependency:

```
<id>  <path>
```

two spaces between them, `\n` after each line. The first line is the package
itself with its source commit. The id of a dependency depends on where the RPM
comes from:

| The RPM comes from | id | nested lines below it |
|---|---|---|
| the SLFO 1.1 bootstrap repository (no source package of the project builds it) | first 32 hex characters of the RPM's sha256 | none |
| a source package of the project (SLFO 1.2) | that package's source commit (64 hex characters, git sha256) | that package's own tree |

Both are sha256. Git sha256 commit ids are 64 hex characters (256 bits). RPM
ids are cut to 32 hex characters (128 bits) to keep the width of OBS's md5 ids;
they only detect changes, so 128 bits are plenty. No MD5 is used.

The meta hash is the sha256 of this text with a version prefix
(`pkg/scheduler/checker/meta.go`):

```
meta = sha256("bsgo-meta-v2\n" + line1 + "\n" + line2 + "\n" + ...)
```

## 3. A real tree: wdiff

Raw state row:

```
package       | wdiff
state         | scheduled
source_commit | 788150ffc81340e7ed97a6dd6db182f42d83f460032b06ba77cf97ae284fcef8
built_commit  | 788150ffc81340e7ed97a6dd6db182f42d83f460032b06ba77cf97ae284fcef8
meta          | 6f7a0641434f431fcbdefed94cef10428c3928a6cbddbec0b7facae39792a411
built_meta    | c174cf4bd33abcd444ef0770355169fc367b4279de08c5e4b9b36fe6db9d631f
bcnt          | 4
```

Same commit, different meta: wdiff is scheduled because its dependencies changed
(here: glibc's first SLFO 1.2 build), not its source.

The stored tree of the last build (`build_meta`, key = `built_meta`) is 6002
bytes gzip, 236 lines: 87 direct dependencies, 122 at depth 2, 25 at depth 3,
3 at depth 4. Excerpt:

```
788150ffc8...ae284fcef8  wdiff                      first line: own source commit
7a815c0770...bfbcf0e6e7  binutils                   built in 1.2: binutils' commit
2fe52b4671...17ef62      cpp-build                  same commit as gcc-build, gcc-build-PIE:
2fe52b4671...17ef62      gcc-build                    all three RPMs come from gcc:build
07d574f8ef...baba9       gcc13                      same commit as cpp13, gcc13-PIE (gcc13)
00aef044b557dd410083d16ff850e821  crypto-policies   32 chars: RPM sha256, no 1.2 build behind it
0d5d28887e477c62b1335e142ee69ee2  glibc-devel       glibc-devel from SLFO 1.1 at that time
33f8b9dbeb...e1d03a1     binutils/expect/tcl-devel  nested: see section 4
20e9913537...889198      binutils/tcl/timezone
```

Even small packages have ~230 lines, because every build environment contains
the toolchain (`Required: gcc-PIE` pulls gcc:build and gcc13, plus binutils,
rpm-build, ...).

### Proof that it really is text

```
$ ... | xxd -r -p | od -c          (stored bytes start with 1f 8b 08: gzip)
7 8 8 1 5 0 f f c 8 1 3 4 0 e 7 ... (64 hex) ␣ ␣ w d i f f \n 7 a 8 1 5 ...

$ ... | cat -A                      ($ = end of line)
2d4e46af...a8c9  binutils/expect$
33f8b9db...03a1  binutils/expect/tcl-devel$

$ { printf "bsgo-meta-v2\n"; cat wdiff.tree; echo; } | sha256sum
c174cf4bd33abcd444ef0770355169fc367b4279de08c5e4b9b36fe6db9d631f    = wdiff's built_meta
```

Paths are built by plain string operations (`addMeta`,
`pkg/scheduler/checker/metatree.go`): each line of the producer's tree gets
`<rpm-name>/` inserted after the two spaces, so binutils' line
`33f8b9db...  expect/tcl-devel` becomes `33f8b9db...  binutils/expect/tcl-devel`
in wdiff's tree. OBS `.meta` files have exactly the same lines (with md5).

To look at a tree yourself on the server:

```bash
podman exec bsgo-postgres psql -U bsgo -d bsgodb -tAc \
  "select encode(lines,'hex') from build_meta where hash=(select built_meta
   from package_build_state where project='SUSE:SLFO:1.2' and arch='x86_64'
   and package='wdiff' and flavor='')" | xxd -r -p | gunzip | head -30
```

Use `meta` instead of `built_meta` to get the tree of the next build; `diff` the
two to see why a package is scheduled.

## 4. How deep the tree goes

In principle all the way down. When wdiff uses the RPM `binutils`, the checker
pastes binutils' whole tree under the `binutils` line, and binutils' tree
already contains the trees of its own dependencies. Reading
`binutils/expect/tcl-devel`:

- wdiff was built with the RPM `binutils`;
- binutils was built with the RPM `expect` (its test suite needs it);
- expect was built with the RPM `tcl-devel`.

What keeps trees small is deduplication (section 7): a line survives only at its
shallowest position. `tcl-devel` stays at depth 3 only because nothing closer to
the top of wdiff's tree contains it.

The depth is the point of the transitive rebuild policy SLFO uses: if tcl
changes, expect is rebuilt, then binutils, then wdiff; the deep lines let the
checker see that chain.

## 5. Moving from SLFO 1.1 to 1.2 binaries (made-up example)

Ids shortened. `foo` build-requires `glibc-devel` and `zlib-devel`; glibc still
comes from 1.1, zlib is built by the project.

```
c0ffee01  foo                      own commit
a1a1a1a1  glibc-devel              1.1 RPM: sha256, nothing nested
7e7e7e7e  zlib-devel               our zlib: zlib's commit
5a5a5a5a  zlib-devel/gcc13         how zlib was built (zlib's tree, nested)
```

Our glibc builds; its RPMs hide the 1.1 ones with the same name:

```
c0ffee01  foo
9b9b9b9b  glibc-devel              now glibc's commit
d1d1d1d1  glibc-devel/python3-base glibc's own tree, nested
7e7e7e7e  zlib-devel
5a5a5a5a  zlib-devel/gcc13
```

The `glibc-devel` line changed, so the meta changed: foo is rebuilt against the
1.2 glibc. That is what bootstrapping means.

## 6. Ripple to dependents

`bar` needs `foo-devel`. Before foo's rebuild:

```
b4b4b4b4  bar
c0ffee01  foo-devel
a1a1a1a1  foo-devel/glibc-devel    foo's old nested line
```

After foo has rebuilt:

```
b4b4b4b4  bar
c0ffee01  foo-devel                same commit: foo's source did not change
9b9b9b9b  foo-devel/glibc-devel    but foo's nested tree did
```

bar is rebuilt although it never touched glibc directly.

## 7. Order, deduplication, cycle cut

**Order.** While a dependency is being rebuilt, a package waits (`blocked`), so
it rebuilds once after all its dependencies are done, not once per dependency:

```
glibc   building
foo     blocked: glibc (building)
bar     blocked: foo (blocked)
```

**Deduplication.** Raw lines before deduplication:

```
9b9b9b9b  glibc-devel              depth 1
9b9b9b9b  zlib-devel/glibc-devel   depth 2, same id, same RPM name
```

Only the shallowest copy of each (id, RPM name) pair is kept, so a glibc change
counts once, however many paths lead to glibc.

**Cycle cut.** gcc13 uses `libgcc_s1`, built by gcc16; gcc16 is compiled with
gcc13. Raw tree of gcc13:

```
13131313  gcc13
16161616  libgcc_s1                   built by gcc16
13131313  libgcc_s1/gcc13-PIE         gcc16 was built with gcc13 itself
13131313  libgcc_s1/gcc13-PIE/...     and everything below
a1a1a1a1  libgcc_s1/glibc-devel
```

Paths that go through gcc13's own RPMs are cut:

```
13131313  gcc13
16161616  libgcc_s1
a1a1a1a1  libgcc_s1/glibc-devel
```

In plain words, think of each tree as a recipe card listing the ingredients and
how they were made:

- gcc13 is rebuilt. gcc16 sees a new ingredient (gcc13) and rebuilds. gcc16's
  card changes only in the part "made using gcc13". gcc13 ignores mentions of
  itself, sees no other change, and does not rebuild again: no ping-pong.
- glibc changes. gcc16 rebuilds; its card now says "made with the new glibc".
  That part is not about gcc13, so gcc13 sees a real change and rebuilds, which
  is correct: its libraries come from a gcc16 built against another glibc.

Changes that come back to you from yourself are ignored; changes from anyone
else count.

## 8. The migration case (builds older than the trees)

Before 2026-11-17 bsgo stored only one fingerprint per build, not the list of
what was used. That fingerprint cannot be opened and pasted into another
package's tree. When trees were introduced, about 570 packages had last been
built before that date: they had no tree.

For a dependency built by such a package, the checker could only write a
placeholder line with nothing below it:

```
c0ffee01  foo
4f4f4f4f  zlib-devel               zlib built 2026-11-16, no tree: placeholder
```

Later zlib is rebuilt for an ordinary reason and from then on has a tree. The
next time foo's tree is computed, zlib is described properly:

```
c0ffee01  foo
7e7e7e7e  zlib-devel               zlib's commit
5a5a5a5a  zlib-devel/gcc13         its tree appears below
```

foo's tree changed, so foo was rebuilt, although nothing that matters changed:
only the way zlib is described. This happens once per such package, the first
time it rebuilds; the ~570 packages did not rebuild at once, and many of them sit
deep under the toolchain (pam, libeconf, binutils, diffutils, ninja, ...), so
each first rebuild cost a full gcc13/gcc15/gcc16 round (~5 hours on aisrv).

Mitigation (2026-11-19, `built_commit`): the placeholder is now the commit the
package was built from, which is exactly what its future tree starts with. When
the tree appears, the line stays the same; only the nested lines are added, and
many of those are already present higher up (deduplication).

The built commit of old builds was only recorded for packages that were in state
`succeeded` when the column was added (for other states the stored commit may be
a newer, never built one). That morning 156 core packages were `failed` because
src.opensuse.org was down, so they kept the old placeholder behaviour, once
each. The effect disappears for good once every package has been rebuilt at
least once after 2026-11-17.

## Code to read

| Topic | bsgo | OBS |
|---|---|---|
| tree construction, nesting | `pkg/scheduler/checker/metatree.go` (`addMeta`, `genMeta`, `diffDepth`) | `BSBuild.pm` (`add_meta`, `gen_meta`), `BSSolv` |
| meta of a unit, fallback lines | `pkg/scheduler/checker/meta.go` | `BSSched/BuildJob/Package.pm` (`check`) |
| cycles, cycle levels | `pkg/scheduler/checker/decide.go` | `BSSched/Checker.pm` (`handlecycle`) |
| storage | `pkg/storage/postgres` (`build_meta`, `package_build_state`) | `:meta/<package>` files |
