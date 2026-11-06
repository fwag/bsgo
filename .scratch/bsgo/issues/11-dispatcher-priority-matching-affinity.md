# 11: Dispatcher: Priority Matching, Warm-Cache Affinity, and Job Leasing

**What to build:**
Implement the `bsgo-dispatcher` service. The Dispatcher consumes unassigned jobs from NATS `JOBS_PENDING`, maintains an in-memory inventory of connected worker nodes via periodic heartbeats (`bsgo.workers.heartbeat`), and evaluates multi-dimensional hardware constraints (RAM, vCPUs, sandbox type). It prioritizes jobs by topological dependency depth (critical-path prioritization), scores workers by warm-cache affinity (matching local SSD package hashes to chroot requirements), delivers jobs directly to the chosen worker inbox (`bsgo.workers.<id>.job`), and publishes cancellation signals (`bsgo.jobs.<id>.control`) for superseded commits.

**Blocked by:** 10: Scheduler: Git Webhook to NATS Job Pipeline

**Status:** ready-for-agent

- [ ] Ingestion and tracking of worker heartbeats (`WorkerHeartbeat` Protobuf) with hardware capacity and cached RPM hashes.
- [ ] Priority queue ordering pending jobs by dependency depth so blockers compile first.
- [ ] Hardware constraint matcher filtering workers by RAM, vCPUs, disk, and sandbox compatibility.
- [ ] Warm-cache affinity scorer selecting the worker with the highest ratio of cached chroot packages.
- [ ] Point-to-point dispatch delivering jobs directly to `bsgo.workers.<worker_id>.job` with `AckWait` lease renewal.
- [ ] Superseded commit detector publishing `JobControl` (action: `ABORT`) to cancel obsolete builds.
- [ ] Concurrency unit and integration tests verifying constraint filtering, priority scheduling, and cache affinity scoring.
