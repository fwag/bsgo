# 20: Supply Chain: SLSA 1.2 Provenance Attestation Pipeline

**What to build:**
Implement full SLSA 1.2 Build Track compliance across `bsgo-worker` and `bsgo-publisher`. The worker generates an authenticated in-toto v1.0 Statement (`_slsa_provenance.json`) capturing the exact Merkle commit SHA, resolved `libsolv` chroot RPM bill of materials (with cryptographic digests), build entrypoints, and builder platform identity. The attestation is uploaded to S3 alongside artifacts, and verified by the publisher prior to repodata inclusion.

**Blocked by:** 16: Scheduler: Rebuild Policies, Prioritized Ingestion & Admin Trigger Tool

**Status:** closed

- [x] In-toto v1.0 Statement & SLSA 1.2 provenance schema in `pkg/proto/v1`.
- [x] Provenance generation in `pkg/worker/sandbox.go` collecting input hashes, Git Merkle commit, and chroot bill of materials.
- [x] Ephemeral build isolation verification ensuring network cut-off during execution.
- [x] Attestation S3 upload and tamper-evident metadata link in publisher.
- [x] Automated end-to-end verification of provenance generation and cryptographic validity.
