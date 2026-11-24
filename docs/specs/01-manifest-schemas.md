# Specification: Project and Package Manifest Schemas

This document defines the canonical declarative YAML schemas for `bsgo` projects and packages.

---

## 1. Root Project Manifest: `project.yaml`

Located at the root of a project Git repository (`project.yaml`).

### Schema Definition
```yaml
schema_version: "v1"

project:
  name: "openSUSE:Factory"
  title: "openSUSE Factory Distribution"
  description: "Rolling release base distribution"

# Build Configuration (equivalent to OBS prjconf, see ADR 0010).
# Scopes: public_config  -> every Consumer Project that has this project in its path
#         protected_config -> this project and its "<name>:*" subprojects (%_is_in_project)
#         private_config   -> this project only (%_is_this_project)
public_config:
  build_types:
    - "spec"
  build_engine: "build"
  repo_types:
    - "rpm-md"
  defines:                      # top-level %define, visible to directives below
    lua_pref: "lua54"
  prefer:
    - "%{lua_pref}"             # expanded via defines, like OBS
    - "-yast2-schema"           # negative prefer
    - "python3-kiwi:libsystemd0" # context prefer (only when pulled in by python3-kiwi)
  ignore:
    - "broken-pkg"
    - "dracut:systemd-sysvinit" # ignore only when required by dracut
  substitute:
    default-jdk: ["java-17-openjdk-devel"]
  preinstall:
    - "bash"
    - "coreutils"
    - "rpm"
  vminstall:
    - "kernel-default"
  build_flags:
    - "allowrootforbuild"
  release: "<CI_CNT>.<B_CNT>"
  macros: |                     # opaque rpm Macros: body, evaluated by rpm at build time
    %vendor openSUSE
  raw_config: |                 # verbatim prjconf for constructs with no structured field
    %if !0%{?is_stage_project}
    Release: <CI_CNT>.<B_CNT> spec:160000.<CI_CNT>.<B_CNT>
    %endif

protected_config:
  build_flags:
    - "excludebuild:lttng-modules"

private_config:
  macros: |
    %source_date_epoch_from_changelog Y

# Per-architecture overlays (%ifarch); also allowed under targets.<name>.arch_config
arch_config:
  aarch64:
    prefer:
      - "-grub2-x86_64-efi"

targets:
  standard:
    architectures:
      - "x86_64"
      - "aarch64"
      - "ppc64le"
      - "s390x"
    dependencies:
      - project: "openSUSE:Factory:Staging"
        target: "standard"
      - project: "openSUSE:Base"
        target: "standard"
      - url: "https://download.opensuse.org/repositories/openSUSE:/Tools/openSUSE_Factory/"
        type: "rpm-md"
    flags:
      build: true
      publish: true
      debuginfo: true

  images:
    architectures:
      - "x86_64"
      - "aarch64"
    dependencies:
      - project: "openSUSE:Factory"
        target: "standard"
    # Target-specific configuration overrides
    config:
      build_types: ["kiwi", "docker"]
      build_engine: "podman"
      repo_types: ["none"] # Disables RPM repodata generation for raw images
    flags:
      build: true
      publish: true
      debuginfo: false
```

### Field Descriptions
| Field Path | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `schema_version` | string | Yes | Manifest version (`"v1"`). |
| `project.name` | string | Yes | Unique canonical project identifier (e.g. `openSUSE:Factory`). |
| `project.title` | string | No | Short human-readable display title. |
| `project.description` | string | No | Markdown/text description. |
| `public_config` | obj | No | Build Configuration visible to every Consumer Project (OBS prjconf outside `_is_in_project` guards). Deprecated read-only alias: `config`. |
| `protected_config` | obj | No | Build Configuration applied only to this project and its `<name>:*` subprojects (`%_is_in_project`). Deprecated read-only aliases: `protected`, `namespace_scoped`. |
| `private_config` | obj | No | Build Configuration applied only to this project (`%_is_this_project`). Deprecated read-only alias: `private`. |
| `arch_config.<arch>` | obj | No | Architecture overlay (`%ifarch`). Same fields as `*_config`. |
| `*_config.build_types` | list[str] | No | Allowed build recipe types (e.g. `["spec"]`, `["kiwi"]`, `["docker"]`, `["helm"]`). Defaults to `["spec"]`. Renders as `Type:` with the first entry only, because obs-build honours a single `Type:` value. |
| `*_config.build_engine` | string | No | Toolchain execution engine (`"build"`, `"podman"`, `"mock"`, `"helm"`). Defaults to `"build"`. |
| `*_config.repo_types` | list[str] | No | Published repository index formats (`["rpm-md"]`, `["none"]`, `["helm"]`). Defaults to `["rpm-md"]`. |
| `*_config.from_scratch` | bool | No | If `true`, stops config inheritance from dependency targets (clean OBS `FromScratch:` replacement). |
| `*_config.prefer` | list[str] | No | Preferred package list for resolving dependency ambiguities in `libsolv`. |
| `*_config.ignore` | list[str] | No | Package list to ignore during dependency resolution. |
| `*_config.substitute` | map[str, list[str]] | No | Virtual capability to concrete package substitutions. |
| `*_config.conflict` | list[list[str]] | No | Declared mutual exclusions between packages in build chroot. |
| `*_config.preinstall` | list[str] | No | Minimal packages extracted to bootstrap chroot before `rpm` runs (`bash`, `rpm`). |
| `*_config.vminstall` | list[str] | No | Hypervisor packages required to boot worker KVM micro-VM. |
| `*_config.support` | list[str] | No | Packages added to every build environment (`Support:`); `!name` removes an inherited entry. |
| `*_config.required` | list[str] | No | Packages required in every build environment and included in rebuild decisions (`Required:`). |
| `*_config.file_provides` | map[str, list[str]] | No | File path to providing packages (`FileProvides:`). |
| `*_config.expand_flags` | list[str] | No | Dependency expansion flags (`ExpandFlags:`, e.g. `keepfilerequires`). |
| `*_config.export_filter` | list[str] | No | Binary export filters (`ExportFilter:` regex + target arches). |
| `*_config.constraints` | list[str] | No | Worker hardware constraints (`Constraint:`). |
| `*_config.macros_file` | string | No | **Deprecated** (ADR 0010). Previously pointed at a raw prjconf/macros file; importers must convert it into structured fields plus `raw_config`. |
| `*_config.order` | list[str] | No | Forced package installation order to break dependency cycles. |
| `*_config.build_flags` | list[str] | No | Build execution flags (e.g. `allowrootforbuild`). |
| `*_config.publish_flags` | list[str] | No | Publication behavior flags (e.g. `archsync`). |
| `*_config.optflags` | map[str, str] | No | Architecture-specific compiler flags (e.g. `x86_64: "-O2 -g"`). |
| `*_config.defines` | map[str,str] | No | Top-level `%define`s evaluated by the config parser; usable in other directives (`%{name}`). The mapping order is preserved on load and save and is the render order (order matters for `%define`). Names are unique within one scope. |
| `*_config.macros` | string | No | Opaque rpm `Macros:` body passed verbatim to the build toolchain; evaluated by rpm at build time. |
| `*_config.release` | string | No | Release scheme (`Release:` directive, e.g. `<CI_CNT>.<B_CNT>`). |
| `*_config.raw_config` | string | No | Verbatim prjconf fragment appended after the structured fields of the same scope. Escape hatch for constructs without a structured field; importers must use it instead of flattening `%if` branches. |
| `targets.<name>` | map | Yes | Dictionary of build targets defined for this project. |
| `targets.<name>.architectures` | list[str] | Yes | Build architectures supported (e.g. `x86_64`, `aarch64`). |
| `targets.<name>.config` | obj | No | Target overlay (`%_repository` conditionals). Same fields as `*_config`. |
| `targets.<name>.arch_config.<arch>` | obj | No | Target + architecture overlay. |
| `targets.<name>.dependencies` | list[obj] | Yes | Ordered path. The target's own repository always comes first, then these entries in order; a package name found in an earlier entry hides same-named packages in later ones. A Bootstrap Dependency is listed here temporarily. |
| `targets.<name>.dependencies[].project` | string | Conditional | Reference to another `bsgo` project. |
| `targets.<name>.dependencies[].target` | string | Conditional | Target within the referenced project. |
| `targets.<name>.dependencies[].url` | string | Conditional | External RPM repository URL (handled via Download-on-Demand + S3 caching). |
| `targets.<name>.dependencies[].type` | string | Conditional | External repository type (defaults to `rpm-md`). |
| `targets.<name>.dependencies[].name` | string | Conditional | Required for `url` entries: stable `project/repository` name used as `%_repository`/`_project` when layering configs (e.g. `SUSE:SLFO:1.1/standard`). |
| `targets.<name>.flags.build` | bool | No | Master build switch for target (default: `true`). |
| `targets.<name>.flags.publish` | bool | No | Master repository publication switch (default: `true`). |
| `targets.<name>.flags.debuginfo`| bool | No | Whether to extract and publish `debuginfo`/`debugsource` RPMs (default: `true`). |

### Build Configuration Semantics (normative, ADR 0010)

1. **Rendering.** For one path element and one Consumer Project, a scope set
   is selected: `public_config` always; `protected_config` if the consumer
   equals the project or starts with `<project>:`; `private_config` if the
   consumer equals the project. Each selected scope, then `targets.<name>.config`,
   then the matching `arch_config` overlays, is rendered to prjconf text in
   this order: `defines` (as `%define`), directives, `raw_config`,
   `Macros:` block.
2. **Layering.** The rendered texts of all path elements are concatenated in
   reverse path order (the furthest dependency first, the consumer's own
   project last), each preceded by `%define _repository`, `_is_this_project`
   and `_is_in_project`, exactly like OBS `BSSched/ProjPacks.pm:getconfig`.
   `from_scratch: true` discards everything rendered before it. The
   concatenated text is then parsed once. Implementations must not merge
   structured fields directly.
3. **Merge semantics** are those of obs-build `Build::read_config`:
   list directives append (`prefer`, `ignore`, `substitute`, `conflict`,
   `file_provides`, `support`, `required`, `preinstall`, `vminstall`,
   `build_flags`, `expand_flags`, `export_filter`); scalar directives
   override (`release`, `build_engine`, `repo_types`, `build_types`);
   a `!` prefix removes an entry (`support: ["!foo"]`).
4. **Two outputs.** The scheduler parses the text with `Macros:` blocks
   stripped. Workers receive the full text (with `Macros:`) as `build --dist`.
5. **Conformance.** An importer converting an OBS prjconf must pass the
   round-trip test: for every arch × repository × Consumer Project
   (self, subproject, foreign), parsing the rendered config yields the same
   result as parsing the original prjconf through OBS `getconfig`. All fields
   of the parsed config are compared exactly, with these tolerances only:
   parser warnings (they carry line numbers of the generated text); the order
   of the set-like lists `prefer`, `ignore`, `conflict`, `support`,
   `required`, `expandflags`, `buildflags`, `publishflags` (scopes render in
   a fixed order, and OBS uses these lists as sets); and the hash value of
   the multi-valued flags `excludebuild` and `dockerarg` (OBS reads all
   values from the list). Ordered lists such as `preinstall` and `vminstall`,
   `Macros:` text and every scalar must match exactly.
6. **Order safety of importers.** Scopes render in the fixed order above, but a
   prjconf interleaves its statements freely. An importer may store a
   statement structurally only if moving it to its rendered position changes
   nothing: statements that write the same directive, flag name, keyed entry
   or `%define` (or read a macro that another statement defines) keep their
   relative order. Where the fixed order would invert such a pair, the
   importer keeps both in the public `raw_config` in source order and reports
   them. `bsgo project convert-obs` implements this and runs the gate before it
   writes anything.

---

## 2. Sharded Package Manifest: `packages/xx/<package>.yaml`

Located at `packages/<hash2>/<package_name>.yaml`, where `<hash2>` is `sha256(<package_name>)[:2]`.

### Schema Definition
```yaml
schema_version: "v1"

package:
  name: "chromium"
  title: "Chromium Web Browser"
  description: "Open-source browser project"

source:
  git: "https://github.com/openSUSE/chromium.git"
  branch: "factory"
  # commit: "8f7e2a9b4c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f" # Optional exact SHA pin
  recipe: "chromium.spec" # Optional, auto-detected if omitted

build:
  enabled: true
  targets:
    standard: true
    images: false
  architectures:
    exclude:
      - "s390x"
  constraints:
    sandbox: "kvm"       # run only on a worker with a kvm sandbox

resources:
  memory: "32Gi"
  vcpus: 16
  disk: "100Gi"
  timeout: "8h"
```

### Field Descriptions
| Field Path | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `schema_version` | string | Yes | Manifest version (`"v1"`). |
| `package.name` | string | Yes | Unique package name matching the YAML filename. |
| `package.title` | string | No | Short title. |
| `package.description` | string | No | Package summary. |
| `source.git` | string | Yes | Git clone URL for package source repository. |
| `source.branch` | string | Conditional | Git branch to track for builds (mutually exclusive with `commit`). |
| `source.commit` | string | Conditional | Exact Git commit SHA pin for reproducible builds. |
| `source.recipe` | string | No | Relative path to recipe (`.spec`, `Dockerfile`, `config.xml`). Auto-detected if omitted. |
| `build.enabled` | bool | No | Enable/disable builds for this package across all targets (default: `true`). |
| `build.targets` | map[str, bool]| No | Per-target enable/disable overrides (e.g. `images: false`). |
| `build.architectures.include` | list[str] | No | Whitelist of architectures to build. |
| `build.architectures.exclude` | list[str] | No | Blacklist of architectures to skip (e.g. `[s390x]`). |
| `build.constraints.sandbox` | string | No | Sandbox the job needs, overriding what OBS constraints derive: space separated names (`kvm`, `nspawn`, `chroot`, `secure`; a leading `!` excludes, e.g. `secure !chroot`), or `none` to drop the constraint. See below. |
| `resources.memory` | string | No | Memory limit for worker VM (e.g. `16Gi`, default `4Gi`). |
| `resources.vcpus` | int | No | CPU cores allocated to worker VM (default `2`). |
| `resources.disk` | string | No | Root/swap disk size allocated to worker VM (default `20Gi`). |
| `resources.timeout` | string | No | Max execution time before killing worker (default `2h`). |

### Build Constraints (OBS compatible)

Per-package constraints route jobs to suitable workers, for example packages
whose test suite needs a real VM kernel to KVM workers (util-linux needs
UDP-Lite, which the nspawn worker's host kernel may lack). bsgo reads the same
sources as OBS: the `Constraint:` lines of the project configuration, the
package `_constraints` file (including `<overwrite><conditions>` per arch and
package) and `#!BuildConstraint:` in the recipe, merged with the OBS
precedence (project < `_constraints` < recipe). `build.constraints.sandbox` is
the bsgo-native override applied on top: it replaces the derived sandbox
constraint (`none` removes it). The wire form and the enforced subset are
specified in spec 02, section 3.2.

`bsgo project import-obs-git` reads an optional `_bsgo/packages.yaml` from the
imported ref and applies it to the generated package manifests, so overrides
live in the OBS project git:

```yaml
packages:
  util-linux:
    build:
      constraints:
        sandbox: kvm
  llvm19:
    resources:        # same fields and format as the manifest `resources:`
      vcpus: 8
      memory: 24Gi
```

Supported keys per package: `build.constraints.sandbox` and `resources`
(`memory`, `vcpus`, `disk`, `timeout`; set fields replace the generated
manifest's, values are validated at import). Unset `timeout` means no limit.
Unknown keys are rejected; entries for packages that were not imported are
reported (stderr and `unused_overrides` in `--json`).

---

## 3. Operational Ingestion & GitOps Mutation Flow

```
┌─────────────────┐        Git Webhook        ┌─────────────────────────┐
│   Git Forge     ├──────────────────────────►│      bsgo API /         │
│ (GitHub/Gitea)  │◄──────────────────────────┤   Ingestion Service     │
└────────┬────────┘      API Commit & Push    └────────────┬────────────┘
         │ (project.yaml,                                  │
         │  packages/xx/*.yaml)                            ▼
         │                                    ┌─────────────────────────┐
         │                                    │   PostgreSQL            │
         │                                    │ (Operational Projection)│
         │                                    └────────────┬────────────┘
         │                                                 │
         ▼                                                 ▼
┌─────────────────┐                           ┌─────────────────────────┐
│ Git Audit Trail │                           │     bsgo Scheduler      │
│  (Merkle Tree)  │                           │   (libsolv SAT Engine)  │
└─────────────────┘                           └─────────────────────────┘
```

1. **User / Agent Command:**
   ```bash
   bsgo package set-resources chromium --memory 32Gi --vcpus 16
   ```
2. **API Execution:**
   - Computes shard path: `sha256("chromium")[:2]` = `packages/e8/chromium.yaml`.
   - Modifies YAML payload.
   - Creates a signed Git commit via Git Forge API and pushes it to `main`.
3. **Database Projection:**
   - Webhook triggers ingestion service.
   - Validates manifest against JSON schema.
   - Projects updated fields into PostgreSQL `project_packages` table.
4. **Scheduler Action:**
   - Scheduler evaluates change and updates worker dispatch constraints.
