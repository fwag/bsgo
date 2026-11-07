# AGENTS.md - Build Service Go (bsgo)

Core engineering mandates and guardrails for human engineers and AI software factory agents operating in this repository.

## 1. Architectural Independence & Legacy Scrutiny (Human-in-the-Loop)

Never blindly copy or port legacy 20-year-old OBS backend patterns. You must pause, propose modern cloud-native alternatives, and consult the human in the loop if any legacy decision:
- **Impairs Horizontal Scalability**: Any pattern requiring shared POSIX storage (NFS/CIFS), single-threaded central polling loops, or unpartitioned sequential queues.
- **Compromises Security or Isolation**: Any pattern relying on weak container isolation for untrusted compilation payloads, unauthenticated endpoints, or lack of defense-in-depth (e.g. running control plane without gVisor).
- **Increases Maintenance Complexity**: Any monolithic service coupling dependency solving, worker dispatch, and source management into one codebase.

Refer to established system ADRs in `docs/adr/` before proposing structural alterations.

## 2. Library Selection Criteria

Before adding any external third-party Go package or dependency to `go.mod`:
1. **Active Maintenance**: The repository must be actively maintained with recent releases, healthy commit activity, and active issue triaging.
2. **Ecosystem Standard**: Prefer industry-standard, battle-tested libraries (e.g., Charm CLI suite, Cobra, NATS client, pgx, libsolv C bindings). Avoid abandoned, niche, or single-maintainer side projects.
3. **Audit Verification**: Verify the library with `go vet` and vulnerability checks before committing.

## 3. Mandatory Quality & Testing Gates

All production code and pull requests must pass the following pipeline gates before completion:
- **Linting**: Clean execution of `golangci-lint run` (configured in `.golangci.yml`) with zero suppressed warnings or ignored errors.
- **Security Review**: Clean execution of `govulncheck ./...` and `gosec ./...`.
- **Unit Testing**: Table-driven unit tests for all domain packages, achieving comprehensive branch coverage without relying on mocking frameworks where real in-memory types suffice.
- **Integration Testing & Container Runtime**: End-to-end verification against local containerized dependencies (NATS, PostgreSQL, Garage S3). **Strictly use Podman and Podman Compose** (`podman compose`, `podman-compose`, or `hack/mini-setup/`) for all container orchestration. **Never use Docker or invoke the `docker` CLI command.**

## 4. Reviewability & Cognitive Ergonomics (The evergit Standard)

Code must be intentionally engineered to be easy for humans and AI agents to review:
- **Short Functions (`funlen`)**: Maximum 80 lines and 50 statements per function. Break long monolithic procedures into cohesive private helpers.
- **Shallow Nesting (`nestif`)**: Maximum conditional nesting depth of 5. Prefer guard clauses and early returns.
- **Cognitive Simplicity (`gocognit`)**: Penalize deeply nested branching, keeping cognitive complexity score under 20.
- **Zero Duplicate Code (`dupl`)**: Extract repetitive logic blocks (threshold 100 tokens).
- **Enforced via `.golangci.yml`**: Modeled after `evergit` to strictly enforce these reviewability bounds in CI.

## 5. AI-Ready Software Factory & Agent Ergonomics

The entire `bsgo` ecosystem must be architected from day one for seamless, safe operation by autonomous AI agents in software factories:

- **Comprehensive CLI Help & Introspection**: Every command must provide rich, self-documenting `--help` text with explicit intent, valid flag values, and concrete usage examples. Agents discover and reason about capabilities through `--help`.
- **Full Non-Interactive Mutation Coverage**: Every system state that an agent can inspect must be fully mutable via CLI commands (e.g. updating package resources, toggling builds, configuring targets). There must be **zero interactive-only dead ends**; provide `--yes`/`-y` flags to bypass interactive confirmations.
- **Deterministic Machine Contracts (`--json`)**: When invoked with `--json`, commands must emit strictly deterministic JSON on `stdout` with **zero ANSI escapes**, no progress spinners, and no human decoration. Operational diagnostics and error messages must be strictly isolated to `stderr`. Support `--dry-run` where mutations occur.
- **Emoji & Formatting Boundary (TUI Only)**: Emojis and decorative styling are strictly confined to human-facing interactive TUI components (e.g. Lipgloss/Bubble Tea in `pkg/cli/`). **NEVER use emojis or decorative glyphs in operational logs, server logs, worker logs, stderr diagnostics, or daemon outputs.** System logs must remain clean, plain, and machine-parseable by log forwarders and AI agents.
- **Fine-Grained Token ACLs (Least-Privilege Agent Scopes)**: To ensure safe agent execution and contain blast radius, API tokens must enforce fine-grained, resource-scoped permissions:
  - Format: `<action>:<resource>[:<id>]` (e.g. `build:trigger:openSUSE:Factory`, `project:write:home:myuser`, `package:resources:update`).
  - An agent operating on project `home:bot` must be restricted to tokens with `project:write:home:bot`, preventing accidental modifications to core distribution repositories like `openSUSE:Factory`.
- **Deep Modules & Idiomatic Go**: Expose clean, compact interfaces that hide implementation complexity behind stable package boundaries.
- **No Suppression**: Never bypass the type system, disable linters, suppress errors, or use empty catch blocks.
