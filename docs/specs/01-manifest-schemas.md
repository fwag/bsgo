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
| `targets.<name>` | map | Yes | Dictionary of build targets defined for this project. |
| `targets.<name>.architectures` | list[str] | Yes | Build architectures supported (e.g. `x86_64`, `aarch64`). |
| `targets.<name>.dependencies` | list[obj] | Yes | Ordered dependency layers for `libsolv`. Evaluated in order: top entry has highest priority. |
| `targets.<name>.dependencies[].project` | string | Conditional | Reference to another `bsgo` project. |
| `targets.<name>.dependencies[].target` | string | Conditional | Target within the referenced project. |
| `targets.<name>.dependencies[].url` | string | Conditional | External RPM repository URL (handled via Download-on-Demand + S3 caching). |
| `targets.<name>.dependencies[].type` | string | Conditional | External repository type (defaults to `rpm-md`). |
| `targets.<name>.flags.build` | bool | No | Master build switch for target (default: `true`). |
| `targets.<name>.flags.publish` | bool | No | Master repository publication switch (default: `true`). |
| `targets.<name>.flags.debuginfo`| bool | No | Whether to extract and publish `debuginfo`/`debugsource` RPMs (default: `true`). |

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
| `resources.memory` | string | No | Memory limit for worker VM (e.g. `16Gi`, default `4Gi`). |
| `resources.vcpus` | int | No | CPU cores allocated to worker VM (default `2`). |
| `resources.disk` | string | No | Root/swap disk size allocated to worker VM (default `20Gi`). |
| `resources.timeout` | string | No | Max execution time before killing worker (default `2h`). |

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
