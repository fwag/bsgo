# Issue 06: Prototype Modern CLI and Agent Interface

Status: closed
Assignee: gemini
Labels: `wayfinder:prototype`

## Question

How should the `bsgo` CLI command structure, interactive TUI (using Charm `bubbletea`), and non-interactive machine-readable modes (`--json`, quiet execution) be designed to provide an ergonomic experience for developers while enabling automated operation by CI pipelines and LLM agents?

## Resolution

Resolved in [prototype/cli/](../../prototype/cli/) and formalized in [docs/specs/03-cli-and-agent-interface.md](../../docs/specs/03-cli-and-agent-interface.md).
- Dual-Mode Contract: Human TTY mode provides rich Charm `bubbletea`/`lipgloss` styling and an interactive live dashboard (`bsgo watch`). Machine mode (`--json`) emits deterministic JSON on stdout with zero ANSI escapes, zero interactive prompts, and error isolation to stderr (optimized for CI and LLM agents).
- Command Taxonomy: Clean hierarchy (`bsgo project`, `bsgo package`, `bsgo build`, `bsgo watch`).
- API-Assisted GitOps Abstraction: CLI commands like `bsgo package set-resources` automatically compute the hash shard path (`packages/xx/<package>.yaml`) and commit directly through the API, returning structured commit metadata to the user or agent.
- Live Log Streaming: Built-in Core NATS log streaming simulation (`bsgo build logs -f <job-id>`).
- Runnable Prototype: Built and tested in `prototype/cli/` (`go test -v ./...` passes cleanly).
