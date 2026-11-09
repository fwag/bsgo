# 30: Scheduler: Config DAG Inheritance, FromScratch & BuildType Filtering

**What to build:**
Update the `bsgo` scheduler (`pkg/scheduler`) to resolve target configuration along the dependency DAG and enforce recipe build type filtering:
1. **DAG Config Resolution (`FromScratch` Support)**:
   - Walk the target's `dependencies:` list in topological order (base layers to child).
   - If a target declares `from_scratch: true`, terminate upstream dependency walk and initialize the configuration exclusively from that target.
   - Otherwise, merge parent dependency target configurations into the child target's effective configuration.
2. **BuildType Recipe Filtering**:
   - Inspect package source recipes (`.spec`, `.kiwi`, `Dockerfile`, `Chart.yaml`, `.productcompose`).
   - Match discovered recipe types against the target's resolved `BuildTypes` (e.g. `["kiwi"]` vs `["spec"]`).
   - Skip scheduling packages that do not match the target's permitted build types (e.g. ignore `.spec` files in targets dedicated to container images or appliance builds).
3. **Dispatch Contract Extension**:
   - Extend `v1.BuildJob` in `pkg/proto/v1/messages.go` with `BuildEngine`, `BuildType`, and `EffectiveConfig`.
   - Forward the resolved build configuration over NATS JetStream to workers.

**Blocked by:** 29: Manifest: Project & Target Config Schema, Hierarchy & Merging

**Status:** resolved

- [x] Implement `ResolveTargetConfig(targetName string, prj *manifest.ProjectManifest) ProjectConfig` in `pkg/scheduler` with `from_scratch` cutoff logic.
- [x] Add recipe type matching and filtering against target `BuildTypes` in `pkg/scheduler/scheduler.go`.
- [x] Extend `v1.BuildJob` in `pkg/proto/v1/messages.go` with `BuildEngine` and `BuildType`.
- [x] Unit tests in `pkg/scheduler/scheduler_test.go` verifying DAG inheritance, `FromScratch` cutoff, and build type recipe filtering.
