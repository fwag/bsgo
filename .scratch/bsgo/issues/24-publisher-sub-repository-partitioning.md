# 24: Publisher: Sub-Repository Partitioning (Standard, Debuginfo, Sources)

**What to build:**
Implement automated sub-repository partitioning (equivalent to legacy `splitdebug` and `sourcepublish`) matching standard Linux distribution expectations:
1. **Artifact Categorization**: Inspect incoming RPM package headers and filenames in `PublishRequest` to classify artifacts:
   - Source RPMs (`*.src.rpm`) -> Source sub-repository (`repos/<project>/<target>/src/`).
   - Debuginfo & debugsource RPMs (`*-debuginfo-*.rpm`, `*-debugsource-*.rpm`) -> Debug sub-repository (`repos/<project>/<target>/debug/`).
   - Standard binary RPMs -> Runtime sub-repository (`repos/<project>/<target>/<arch>/`).
2. **Sub-Repository Isolated Metadata Generation**: Execute `createrepo_c` independently for each sub-repository with its own `repodata/` directory and GPG signature.
3. **Atomic S3 Sync per Sub-Repository**: Commit each sub-repository atomically using commit-by-pointer without cross-repository metadata contamination.
4. **Client-Facing Repositories**: Ensure DNF, Zypper, and container builders can subscribe to `standard` without downloading massive debuginfo metadata.

**Blocked by:** 23: Publisher: Standalone bsgo-publisher Daemon & Worker Decoupling

**Status:** resolved

- [x] Add RPM classifier categorizing artifacts into standard, debug, and source sub-repositories.
- [x] Update `PublishRequest` and `PublishResult` in `pkg/publisher` to support sub-repository partitioning (`SubRepositories`).
- [x] Run isolated `createrepo_c` passes for `standard`, `debug`, and `src` sub-repositories.
- [x] Upload distinct `repodata/` sets to corresponding S3 paths (`.../<arch>/`, `.../debug/`, `.../src/`).
- [x] Integration tests verifying Zypper/DNF repo compatibility and clean separation of debuginfo packages.
