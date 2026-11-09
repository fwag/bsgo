# 29: Manifest: Project & Target Config Schema, Hierarchy & Merging

**What to build:**
Extend `project.yaml` in `pkg/manifest/manifest.go` with a dedicated, type-safe `ProjectConfig` schema replacing legacy OBS `_config` (`prjconf`):
1. **`ProjectConfig` Schema**:
   - `BuildTypes []string`: Recipe systems allowed/processed (`["spec"]`, `["kiwi"]`, `["docker"]`, `["helm"]`, `["productcompose"]`).
   - `BuildEngine string`: Toolchain/runtime engine (`"build"`, `"podman"`, `"mock"`, `"helm"`).
   - `RepoTypes []string`: Publication index formats (`["rpm-md"]`, `["none"]`, `["helm"]`).
   - `FromScratch bool`: When `true`, stops config inheritance from dependency targets (clean replacement for OBS `FromScratch:`).
   - `Prefer []string`, `Ignore []string`, `Substitute map[string][]string`, `Conflict [][]string`: Solver directives for dependency resolution.
   - `Preinstall []string`, `VMinstall []string`: Chroot extraction and VM boot package lists.
   - `BuildFlags []string`, `PublishFlags []string`, `Optflags map[string]string`: Toolchain and publication flags.
2. **Hierarchical Configuration**:
   - Root `config: ProjectConfig` on `ProjectManifest` declaring project-wide defaults.
   - Target-level `config: ProjectConfig` on `BuildTarget` declaring target-specific overrides (replacing legacy OBS `%if "%_repository" == "images"` conditionals).
3. **Hierarchical Merging**:
   - Implement `ProjectConfig.Merge(override ProjectConfig) ProjectConfig` with deterministic slice deduplication and map overlay.
4. **Specification & Validation**:
   - Update `docs/specs/01-manifest-schemas.md` with schema documentation, field descriptions, and YAML examples.
   - Add unit tests verifying parsing of root config, target overrides, and hierarchical merging.

**Blocked by:** none

**Status:** resolved

- [x] Define `ProjectConfig` struct with full YAML serialization tags in `pkg/manifest/manifest.go`.
- [x] Add `Config *ProjectConfig` to `ProjectManifest` (root project defaults).
- [x] Add `Config *ProjectConfig` to `BuildTarget` (per-target overrides).
- [x] Implement `ProjectConfig.Merge(override ProjectConfig) ProjectConfig`.
- [x] Update `docs/specs/01-manifest-schemas.md` with configuration schema specification.
- [x] Table-driven unit tests in `pkg/manifest/manifest_test.go` covering full parsing, default values, and hierarchical merging.
