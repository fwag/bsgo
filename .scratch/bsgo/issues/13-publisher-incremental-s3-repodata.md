# 13: Publisher: Incremental S3 Repodata Generation & Atomic Publication

**What to build:**
Implement the `bsgo-publisher` service. The publisher consumes `PublishRequest` messages from NATS JetStream `PUBLISH` stream. It serializes updates per `(project, target, arch)` using PostgreSQL advisory locks (`pg_advisory_xact_lock`), syncs only the existing `repodata/` XML metadata (~5-20MB) from S3/Garage, stages newly built RPMs locally, and executes `createrepo_c --update --recycle-pkglist --no-database` to generate incremental metadata without downloading historical binaries. It uploads new hash-prefixed XML files and atomically updates `repodata/repomd.xml` via commit-by-pointer on S3/Garage.

**Blocked by:** 12: Worker: bsgo-worker Execution Sandbox & S3 Upload

**Status:** ready-for-agent

- [ ] Publisher consumer processing `PublishRequest` messages from NATS JetStream `PUBLISH` stream.
- [ ] Concurrency serializer acquiring PostgreSQL transactional advisory locks per repository partition.
- [ ] Metadata-only S3 syncer downloading existing XML files from `repodata/` without historical RPM binaries.
- [ ] Subprocess executor running `createrepo_c` with `--update`, `--recycle-pkglist`, `--cachedir`, and `--no-database`.
- [ ] Atomic S3 publisher uploading hash-prefixed XML files first, followed by atomic overwrite of `repomd.xml`.
- [ ] Garbage collector asynchronously purging orphaned superseded metadata files.
- [ ] Integration tests verifying incremental repodata generation, locking concurrency, and client consumption using `createrepo_c` and Garage S3.
