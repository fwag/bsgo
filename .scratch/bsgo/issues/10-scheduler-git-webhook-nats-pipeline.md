# 10: Scheduler: Git Webhook to NATS Job Pipeline

**What to build:**
Implement the `bsgo-scheduler` service and NATS JetStream event pipeline. The scheduler listens for Git webhook events from external forges (Gitea/GitHub) or CLI triggers (`bsgo package trigger`) on the `TRIGGERS` stream. It invokes the in-process `libsolv` solver, calculates the build dependency graph, and produces an immutable `BuildJob` Protocol Buffer message containing source commit, exact pre-resolved chroot RPM URLs and SHA-256 hashes, hardware resource constraints, and S3 artifact destination. The job is published to the durable JetStream `JOBS_PENDING` stream (`bsgo.jobs.pending`).

**Blocked by:** 09: Scheduler: libsolv CGO Deterministic Solver Engine

**Status:** closed

- [x] NATS JetStream setup configuring `TRIGGERS` (Limits, 7 days) and `JOBS_PENDING` (WorkQueue, 14 days) streams.
- [x] Protobuf message definitions compiled for Go (`BuildTrigger`, `BuildJob`, `ChrootRPM`).
- [x] Webhook ingestion handler and CLI `bsgo package trigger` command emitting `BuildTrigger`.
- [x] Scheduler reconciliation loop detecting source changes and invoking `pkg/solv`.
- [x] Deterministic `BuildJob` generator calculating topological dependency depth (`priority`) and chroot RPM bill of materials.
- [x] Integration tests verifying the complete flow from Git webhook $\to$ `libsolv` expansion $\to$ NATS `BuildJob` emission.
