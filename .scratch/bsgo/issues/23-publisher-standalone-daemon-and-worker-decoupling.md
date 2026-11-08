# 23: Publisher: Standalone bsgo-publisher Daemon & Worker Decoupling

**What to build:**
Decouple repodata generation and repository publishing from the worker execution sandbox into a standalone `bsgo-publisher` daemon:
1. **Worker Decoupling**: Update `bsgo-worker` to upload compiled RPM artifacts directly to `s3://<bucket>/artifacts/<project>/<package>/` and emit a `PublishRequest` event to NATS JetStream `PUBLISH` stream (`bsgo.publish.rpm.<project>.<target>.<arch>`), removing local `createrepo_c` from `pkg/worker/sandbox.go`.
2. **Standalone Daemon (`cmd/bsgo-publisher`)**: Implement a scalable Go daemon listening on NATS JetStream `PUBLISH` queue (`StreamPublish`).
3. **Distributed Locking**: Wire PostgreSQL transactional advisory locks (`pg_advisory_xact_lock(hashtext(...))`) into `pkg/publisher` to serialize updates per `(project, target, arch)` partition across multi-instance publisher fleets.
4. **Publication & Cascade Handoff**: The publisher downloads only existing `repodata/` XML metadata from S3, stages new RPMs, executes incremental `createrepo_c`, signs `repomd.xml` via `MetadataSigner`, commits atomically to S3 via commit-by-pointer, and emits `bsgo.events.published` to trigger downstream rebuild cascades.

**Blocked by:** 22: Signer: bsgo-signd Bare-Metal Daemon, Database Keys & EFI Signing

**Status:** resolved

- [x] Update `bsgo-worker` to upload raw RPM artifacts to S3 and emit `PublishRequest` on NATS JetStream `PUBLISH` stream.
- [x] Remove inline `createrepo_c` and S3 repo syncing from `pkg/worker/sandbox.go`.
- [x] Implement `PostgresLockManager` in `pkg/storage/postgres` using `pg_advisory_xact_lock`.
- [x] Create `cmd/bsgo-publisher/main.go` daemon consuming from NATS JetStream `PUBLISH` stream.
- [x] Wire `MetadataSigner` into `bsgo-publisher` to produce `repomd.xml.asc` and export `repomd.xml.key`.
- [x] Publish `bsgo.events.published` on S3 commit, verifying end-to-end trigger of the Cascade Engine.
- [x] Unit and integration tests verifying worker decoupling, JetStream consumption, and PostgreSQL advisory locking.
