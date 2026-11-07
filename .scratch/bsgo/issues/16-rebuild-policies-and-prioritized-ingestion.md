# 16: Scheduler: Rebuild Policies, Prioritized Ingestion & Admin Trigger Tool

**What to build:**
Implement target-level rebuild policies (`rebuild: transitive|direct|local`, `block: all|local|never`) in `project.yaml` manifests and the scheduler engine. Establish a 3-tier prioritized ingestion pipeline mediated via NATS (`bsgo.triggers.high.*`, `bsgo.triggers.med.*`, `bsgo.triggers.low.*`). Integrate anti-starvation interleaving in the scheduler loop and ensure Git commits touching multiple packages are evaluated as atomic batches. Embed administrative trigger tooling (`bsgo admin trigger`) into the CLI to inject high-priority events directly.

**Blocked by:** 15: E2E: Full Distribution Build Pipeline & Live Charm TUI Dashboard

**Status:** closed

- [x] Target manifest schema support for `rebuild` (`transitive`, `direct`, `local`) and `block` (`all`, `local`, `never`) in `pkg/manifest`.
- [x] 3-tier NATS ingestion pipeline supporting `bsgo.triggers.high`, `med`, and `low`.
- [x] Scheduler evaluation logic respecting target rebuild and block modes.
- [x] Scheduler anti-starvation interleaving preventing background sweeps from starving under sustained high-priority load.
- [x] Atomic multi-package commit evaluation in scheduler.
- [x] Embedded CLI admin trigger tool (`bsgo admin trigger`) with `--priority high`, `--target`, `--arch`, and `--reason` flags.
- [x] Unit and integration tests verifying rebuild policy gating, prioritized ingestion, and admin triggers.
