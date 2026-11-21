# Backlog: ideas and follow-ups not yet planned

Short notes on things we decided to do later. Each entry says why it is deferred
and what it would take. Turn an entry into a numbered plan before implementing
it.

## Worker cache advertisement without the NATS payload limit

- **Today:** a worker's heartbeat carries the sha256 of the RPMs in its local
  cache, so the dispatcher can prefer the worker that already has most of a
  job's build dependencies (`calculateCacheScore`). Since 16ac460 the list is
  deduplicated and capped at the 4,096 most recent digests (about 275 KB) to stay
  under NATS's 1 MiB `max_payload`; older cache entries are still used on disk
  but no longer advertised.
- **Idea:** advertise the whole cache as a Bloom filter in the heartbeat (about
  24 KB for 20,000 digests at a 1% false-positive rate); the dispatcher tests each
  job dependency against it. Constant size, no extra request, no new component. A
  false positive only slightly skews a placement hint.
- **Alternative if an exact list is ever needed** (for example for a worker
  view): the worker writes its full list to the NATS Object Store
  (`worker-cache/<worker-id>`) and the heartbeat carries only a version number;
  the dispatcher re-reads the object when the version changes. Not an HTTP
  endpoint on the worker: workers only make outbound connections today.
- **Deferred because:** placement only matters with more than one worker per
  architecture; aisrv has one nspawn worker.

## Full sha256 ids for RPMs in meta trees

- **Today:** in meta trees an RPM without a producer in the project is
  identified by the first 32 hex characters (128 bits) of its sha256, to keep the
  width of OBS's md5 ids (`shortID`, `pkg/scheduler/checker`). Source commits are
  already full sha256 (64 hex characters).
- **Idea:** use the full 64-character sha256 for RPM lines too, so every id in a
  tree is a complete sha256 and can be compared directly with
  `solvable:checksum`, `bininfo.json`, `entries.json` and the job's bdeps.
- **Cost:** every meta changes once, so every built package is rebuilt once. Do
  it together with another change that already forces a full rebuild, or at the
  end of the SLFO 1.2 bootstrap (`bootstrap_stage 0` switch), not in the middle of
  a rebuild wave.

## Doubled flavor in artifact directories

- **Today:** flavored units are stored under
  `artifacts/<prj>/<repo>/<arch>/<pkg>:<flavor>:<flavor>/`: the job's package
  name already contains `:<flavor>` and the worker's `artifactPrefix`
  (`pkg/worker/collect.go`) appends it again. Found during the storage GC work
  (plan 03); GC does not depend on the directory name.
- **Fix:** build the prefix once; migrate existing keys (`bininfo.json`,
  `entries.json`, published references) or let the next rebuild of each flavored
  unit write the new layout while GC removes the old directory after the grace
  period.
