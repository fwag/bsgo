# 33: External Repository Ingestion, Download-on-Demand (DoD) & S3 CAS Caching

**What to build:**
Implement an automated Download-on-Demand (DoD) pipeline for external RPM/DEB repositories declared in project target dependencies:
1. **Repository Metadata Ingestor**: Periodically poll and parse external `repomd.xml` / `primary.xml.gz` or `.solv` files declared in `targets.<name>.dependencies[].url`.
2. **Scheduler Solv Injection**: Register external repository packages in `bsgo-scheduler`'s in-process `libsolv` pool so dependencies provided by external mirrors resolve without `NoProviderError`.
3. **On-Demand Package Fetching & S3 CAS Caching**: When a build job requires packages from an external URL, download only the resolved binary RPMs, verify their cryptographic checksums against the upstream metadata, and cache them in S3 CAS (`repos/external/<hash>/`).
4. **Hermetic Worker Delivery**: Rewrite job `ChrootRPM` locations to point to S3 cached objects so worker sandboxes (especially KVM micro-VMs) execute completely offline without outbound public internet access, preserving SLSA 1.2 hermeticity.

**Blocked by:** 34: Scheduler: Real Chroot SHA-256 Digest Verification & libsolv RPM-MD Parsing

**Status:** resolved

- [x] Implement external repository metadata fetcher and cache in `pkg/storage` / `pkg/scheduler`.
- [x] Bind external repository solvable indices into `libsolv` pool for scheduling.
- [x] Implement on-demand S3 artifact fetcher for external package dependencies.
- [x] Update worker sandbox to pull all external dependencies strictly from S3, eliminating `--repo <url>` network calls.
- [x] Integration tests verifying offline build execution with external dependency URLs.
