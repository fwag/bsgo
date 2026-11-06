# 0007: Scheduler-Dispatcher Split and NATS JetStream Topology

We decided to decouple the build pipeline into two distinct control plane services:
1. **Scheduler (`bsgo-scheduler`)**: Responsible for listening to Git triggers, running in-process `libsolv` dependency calculations, determining rebuild cascades, and compiling deterministic `BuildJob` specifications.
2. **Dispatcher (`bsgo-dispatcher`)**: Responsible for tracking real-time worker fleet capacity, evaluating hardware constraints (RAM, vCPUs, sandbox type), prioritizing critical-path builds (topological dependency depth), scheduling with warm-cache worker affinity, and managing job leases.

Communication between services, workers, and publishers is mediated via NATS JetStream and Core NATS:
- **Pending Jobs**: Schedulers publish resolved jobs to a durable JetStream queue (`bsgo.jobs.pending`).
- **Worker Matching**: The Dispatcher matches jobs to idle workers and delivers the job directly to the designated worker's private inbox (`bsgo.workers.<worker_id>.job`).
- **Heartbeats & Leases**: Workers send heartbeats to `bsgo.workers.heartbeat`. If a worker stops heartbeating, its lease expires and the Dispatcher re-schedules the job.
- **Superseded Preemption**: The Dispatcher sends abort signals (`bsgo.jobs.<job_id>.control`) to immediately cancel builds superseded by newer Git commits. Early preemption of running builds based on priority is explicitly deferred out of scope.
- **Live Logs**: Streamed in real-time via ephemeral Core NATS pub/sub (`bsgo.logs.<job_id>`), avoiding persistent disk overhead when logs are not actively observed, while completed logs are deposited into S3.
