# 24: Publisher: Sub-Repository Channel Partitioning (Standard, Debuginfo, Sources)

**What to build:**
Implement automated multi-channel repository partitioning matching standard Linux distribution expectations:
1. **Artifact Categorization**: Inspect incoming RPM package headers and filenames in `PublishRequest` to classify artifacts:
   - Source RPMs (`*.src.rpm`) -> Source sub-channel (`repos/<project>/<target>/src/`).
   - Debuginfo & debugsource RPMs (`*-debuginfo-*.rpm`, `*-debugsource-*.rpm`) -> Debug sub-channel (`repos/<project>/<target>/debug/`).
   - Standard binary RPMs -> Runtime sub-channel (`repos/<project>/<target>/<arch>/`).
2. **Channel-Isolated Metadata Generation**: Execute `createrepo_c` independently for each channel with its own `repodata/` directory and GPG signature.
3. **Atomic S3 Sync per Channel**: Commit each sub-channel atomically using commit-by-pointer without cross-channel metadata contamination.
4. **Client-Facing Manifests**: Ensure DNF, Zypper, and container builders can subscribe to `standard` without downloading massive debuginfo metadata.

**Blocked by:** 23: Publisher: Standalone bsgo-publisher Daemon & Worker Decoupling

**Status:** open

- [ ] Add RPM classifier categorizing artifacts into standard, debug, and source channels.
- [ ] Update `PublishRequest` and `PublishResult` in `pkg/publisher` to support multi-channel partitioning.
- [ ] Run isolated `createrepo_c` passes for `standard`, `debug`, and `src` channels.
- [ ] Upload distinct `repodata/` sets to corresponding S3 paths (`.../<arch>/`, `.../debug/`, `.../src/`).
- [ ] Integration tests verifying Zypper/DNF repo compatibility and clean separation of debuginfo packages.
