# 08: Foundation: Project and Package Ingestion Gateway with CLI

**What to build:** 
Scaffold the core Go repository structure and implement the foundational API gateway and CLI commands for managing projects and packages. Packagers and automated agents can declare projects (`project.yaml`), define build targets, and configure sharded package manifests (`packages/xx/<package>.yaml`). The API validates manifests against strict JSON schemas, projects records into PostgreSQL, and performs signed Git commits back to the project repository. All commands support both interactive human terminal output and deterministic `--json` output for AI agents, backed by ephemeral local bare Git repos in automated tests.

**Blocked by:** None (can start immediately).

**Status:** closed

- [x] Root Go module initialized with `.golangci.yml` linting, Cobra CLI, and pgx database driver.
- [x] PostgreSQL migration creating `projects` and `project_packages` tables.
- [x] Manifest parser and JSON schema validator for `project.yaml` and sharded `packages/xx/<package>.yaml` (`sha256(name)[:2]`).
- [x] CLI commands `bsgo project list`, `bsgo project show`, `bsgo package list`, `bsgo package show`, and `bsgo package set-resources`.
- [x] Full dual-mode formatting: Lipgloss/Bubble Tea human output on TTY vs deterministic unadorned JSON on `stdout` when invoked with `--json` (errors strictly to `stderr`).
- [x] Integration tests using ephemeral local bare Git repositories (`git init --bare` in `t.TempDir()`) verifying commit creation, manifest parsing, and PostgreSQL projection.
