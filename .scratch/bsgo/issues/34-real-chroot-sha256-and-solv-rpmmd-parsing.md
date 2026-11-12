# 34: Scheduler: Real Chroot SHA-256 Digest Verification & libsolv RPM-MD Parsing

**What to build:**
Upgrade the scheduler dependency solving and artifact attestation pipeline from mock hashes to authentic cryptographic verification:
1. **libsolv RPM-MD Ingestion**: Extend `pkg/solv/c_bridge.c` and `c_bridge.h` to bind `solv/repo_rpmmd.h` (or provide automated `repomd.xml` parsing into solv format) so real RPM repository indices can be loaded directly into memory without synthetic translation.
2. **Authentic SHA-256 Digests in ChrootRPMs**: Replace `"mock-sha256-digest"` in `pkg/scheduler/scheduler.go` with authentic package SHA-256 digests retrieved from repository metadata or S3 object tags.
3. **Worker Pre-Execution Digest Verification**: Ensure `bsgo-worker` validates every downloaded chroot RPM against `ChrootRPM.SHA256` before mounting or installing it into the sandbox build root.

**Blocked by:** 09: Scheduler: libsolv CGO Deterministic Solver Engine

**Status:** resolved

- [x] Add RPM-MD / repomd parsing support in `pkg/solv` CGO bridge.
- [x] Query and populate authentic SHA-256 digests in `BuildJob.ChrootRPMs` during scheduler expansion.
- [x] Verify SHA-256 checksums in worker sandbox before chroot installation.
- [x] Unit tests for real repomd parsing and digest verification.
