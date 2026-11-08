# 26: Publisher: Debounce Batching, Tombstones & S3 Metadata Garbage Collection

**What to build:**
Harden the publisher against mass-rebuild storms and long-term repository bloat:
1. **Debounce Batching Window**: Implement an in-memory or JetStream-backed debounce buffer (configurable 2–5s) coalescing multiple incoming `PublishRequest` events for the same `(project, target, arch)` into a single `createrepo_c` execution.
2. **Package Deletions & Tombstones**: Support `PackageDeletionRequest` events. Maintain package inventory lists so `createrepo_c` drops deleted packages cleanly during `--recycle-pkglist` without leaving orphaned provides.
3. **Asynchronous S3 Metadata Garbage Collection**: Implement a background cleaner routine that inspects `repos/<project>/<target>/<arch>/repodata/`, parses the active `repomd.xml`, and deletes historical hash-prefixed XML files (`<old-hash>-primary.xml.gz`) older than 24 hours.

**Blocked by:** 23: Publisher: Standalone bsgo-publisher Daemon & Worker Decoupling

**Status:** open

- [ ] Implement per-repository debounce window in `bsgo-publisher` to batch bursty build completions.
- [ ] Add `PackageDeletionRequest` message contract and handler for removing deleted packages from repodata.
- [ ] Implement background S3 metadata garbage collector purging unreferenced hash-prefixed XML files older than retention threshold.
- [ ] Integration tests verifying mass-build batching efficiency and tombstone removal.
