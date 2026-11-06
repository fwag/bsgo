# Issue 04: Specify Project and Package Manifest Schemas

Status: closed
Assignee: gemini
Labels: `wayfinder:grilling`

## Question

What is the exact declarative YAML schema for:
1. The root `project.yaml` file (defining project-level build targets, repository flags, and base dependencies)?
2. The sharded package manifests `packages/xx/<package>.yaml` (tracking git repo URL, branch/commit pin, LFS usage, build flags, and package-level build target overrides)?

## Resolution

Resolved in [docs/specs/01-manifest-schemas.md](../../docs/specs/01-manifest-schemas.md) and [docs/adr/0006-gitops-project-declarations-with-api-driven-merkle-commits-and-postgres-projection.md](../../docs/adr/0006-gitops-project-declarations-with-api-driven-merkle-commits-and-postgres-projection.md).
- Root `project.yaml`: Declares `project.name`, `targets` (with supported architectures, ordered `dependencies` supporting internal targets and external RPM URLs for Download-on-Demand + S3 caching, and `flags` for build/publish/debuginfo).
- Package manifest `packages/xx/<package>.yaml`: Sharded by 2-character hex hash prefix (`sha256(pkg)[:2]`). Contains `source` (git URL, branch/commit pin, recipe path), `build` (master toggle, per-target overrides, architecture whitelist/blacklist), and `resources` (memory, vcpus, disk, timeout).
- Hardware constraints are embedded directly in the package manifest (`resources:`) and manageable via API, eliminating the need for `_constraints.yaml`.
- API-assisted GitOps bridge: The `bsgo` API performs signed Git commits and pushes on the user's behalf to avoid manual Git navigation of hash-sharded files, while maintaining Git's Merkle-tree cryptographic auditability and projecting live state into PostgreSQL.
