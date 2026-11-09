# 32: Worker: BuildEngine Sandbox Dispatch & Just-In-Time obs-build Config Rendering

**What to build:**
Enable multi-engine build execution and just-in-time `.build.config` rendering in `pkg/worker`:
1. **Just-In-Time Config Renderer (`pkg/worker/config.go`)**:
   - Implement `RenderBuildConfig(cfg *manifest.ProjectConfig) string` serializing the typed `ProjectConfig` into legacy `obs-build` format (`Preinstall:`, `VMinstall:`, `BuildEngine:`, `Type:`, `Repotype:`, `Prefer:`, `Substitute:`, `buildflags:`).
   - Write `$jobDir/.build.config` before executing `obs-build`, passing it via `--dist $jobDir/.build.config`.
2. **BuildEngine Sandbox Dispatcher (`pkg/worker/sandbox.go`)**:
   - `"build"` (Default): Execute standard `obs-build` / `rpmbuild` toolchain in KVM micro-VM or chroot.
   - `"podman"`: When `BuildType == "docker"`, execute rootless `podman build` inside the worker sandbox to produce OCI/Docker container image tarballs (`.tar`, `.tar.gz`).
   - `"helm"`: When `BuildType == "helm"`, execute `helm package` inside the sandbox to produce `.tgz` chart packages.
3. **Artifact Identification**:
   - Collect appropriate output artifacts based on the engine (`*.rpm`, `*.tar`, `*.tgz`, disk images) for S3 upload.

**Blocked by:** 29: Manifest: Project & Target Config Schema, Hierarchy & Merging, 30: Scheduler: Config DAG Inheritance, FromScratch & BuildType Filtering

**Status:** resolved

- [x] Implement `RenderBuildConfig(cfg *manifest.ProjectConfig) string` in `pkg/worker/config.go`.
- [x] Write `.build.config` in `buildWithKVM` and pass `--dist <path>` to `/usr/bin/build`.
- [x] Add engine branching in `pkg/worker/sandbox.go` supporting `"build"`, `"podman"`, and `"helm"`.
- [x] Update artifact collection in `collectKVMArtifacts` to handle RPMs, container tarballs, and Helm packages.
- [x] Add unit tests in `pkg/worker/worker_test.go` verifying `.build.config` serialization, engine selection, and artifact collection.
