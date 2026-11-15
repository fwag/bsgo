---
status: accepted
---

# 0010: Structured Scoped Build Configuration Rendered to prjconf Text

Amends ADR 0006 (project declarations). Decided 2026-11-15.

The Build Configuration (OBS prjconf) of a Project is declared as structured,
scoped fields in `project.yaml`, alongside the project meta, instead of
storing an opaque prjconf file. The structured form is easier to review,
diff, validate, and mutate non-interactively from the CLI (AGENTS.md §5).
prjconf is a macro language evaluated with context (arch, repository,
consuming project, position in the path), so the structured model must
reproduce OBS semantics exactly. It does so through a renderer, not by
merging Go structs:

- **Scopes follow OBS `getconfig` (BSSched/ProjPacks.pm).**
  `public_config` applies to every consumer that has this project in its
  path. `protected_config` applies only where `%_is_in_project` is 1 (this
  project or a `<project>:*` subproject). `private_config` applies only where
  `%_is_this_project` is 1. `%ifarch` maps to `arch_config.<arch>`, and
  `%_repository` conditionals map to `targets.<name>.config`.
- **Two macro namespaces stay distinct.** `defines` holds top-level
  `%define`s, which are evaluated by the config parser and visible to
  directives such as `Prefer: %{lua_pref}`. `macros` holds the opaque
  `Macros:` body that rpm evaluates at build time.
- **Layering is textual.** Each path element is rendered to prjconf text for
  the given Consumer Project, the texts are concatenated in OBS order, and the
  result is parsed once. Per-field merge semantics (append, override,
  negate/reset) are those of `Build::read_config`.
- **`raw_config` is the escape hatch.** Per scope, it holds verbatim prjconf
  fragments for constructs that do not map cleanly (e.g. `%if
  0%{?is_stage_project}`). Importers must never flatten conditional branches.
- **Correctness gate.** For every arch × repository × Consumer Project, the parsed
  rendered config must equal the parsed original prjconf. This round-trip
  test uses obs-build's own parser as the reference. The reference is
  test-only, so the control plane still carries no Perl.

## Considered Options

- **Verbatim prjconf file as source of truth.** Lossless by construction and
  trivial to sync with OBS upstream, but opaque to validation, review, and
  agent mutation. Rejected in favor of the structured model plus a
  round-trip gate, which gives the same fidelity guarantee.
- **Structured fields merged as Go structs (the first implementation).**
  Rejected. It lost `%if` branches, `%define`s, and `Release:`, and it
  duplicated `Prefer`/`Conflict` lines when layering SUSE:SLFO:1.1 under
  1.2.

## Consequences

- Importing an OBS project requires a converter that passes the round-trip
  test before its output is committed. Every upstream prjconf change needs a
  re-conversion.
- The worker receives rendered, layered prjconf text (`build --dist`), so
  obs-build on workers sees exactly what OBS would give it.
