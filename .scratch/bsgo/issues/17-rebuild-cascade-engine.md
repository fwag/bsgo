# 17: Scheduler: Downstream Rebuild Cascades & Reverse Dependency Engine

**What to build:**
Implement the downstream rebuild cascade engine. When the publisher completes atomic S3 repodata generation, it emits publication events (`bsgo.events.published`). The scheduler consumes these events, consults PostgreSQL dependency relations to identify reverse dependencies (`rprpdeps`), filters candidates against target `rebuild` policies (`transitive` vs `direct` vs `local`), and emits downstream rebuild triggers to the `bsgo.triggers.med` pipeline.

**Blocked by:** 16: Scheduler: Rebuild Policies, Prioritized Ingestion & Admin Trigger Tool

**Status:** closed

- [x] Publisher event emission on successful S3 repodata sync (`bsgo.events.published`).
- [x] Reverse-dependency query engine resolving affected downstream packages.
- [x] Policy-driven cascade filter respecting `rebuild: local` (skipping rebuilds) and `rebuild: direct` (direct BuildRequires only).
- [x] Cascade triggers emitted to `bsgo.triggers.med`.
- [x] Dependency gatekeeping placing packages into `BLOCKED` state when dependencies are actively compiling.
