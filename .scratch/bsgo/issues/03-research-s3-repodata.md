# Issue 03: Research S3 RPM Repodata Generation

Status: closed
Labels: `wayfinder:research`

## Question

What is the most efficient and scalable strategy for generating and incrementally updating RPM repository metadata (`repomd.xml`, `primary.xml.gz`, `filelists.xml.gz`) directly on an S3-compatible object store using `createrepo_c` (or native Go tooling) without requiring full repository downloads on every package publication?

## Resolution

Resolved in [docs/research/03-s3-repodata.md](../../docs/research/03-s3-repodata.md).
- Use `createrepo_c` CLI subprocess wrapper with `--update`, `--recycle-pkglist`, `--cachedir`, and `--no-database`.
- Metadata-only S3 sync: download only `repodata/` (~5-20MB) and stage newly built RPMs locally, avoiding gigabytes of package downloads.
- Concurrency: Serialize per-repository publishing using NATS JetStream partitioned subject queues (`publish.rpm.<prj>.<repo>.<arch>`) and PostgreSQL advisory locks.
- Atomicity: Upload new RPMs and hash-prefixed XML files first, then atomic "commit-by-pointer" upload of `repomd.xml`.
