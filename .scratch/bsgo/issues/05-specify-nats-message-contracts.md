# Issue 05: Specify NATS JetStream Message Contracts

Status: closed
Assignee: gemini
Labels: `wayfinder:grilling`

## Question

What is the exact JSON/Protobuf message schema, stream retention policy, and subject hierarchy for:
1. `build.trigger` (incoming webhook/event to scheduler)?
2. `jobs.<arch>` (scheduler dispatch to worker daemons)?
3. `jobs.status` (worker heartbeat, live log streaming pointers, and completion notifications)?
4. `publish.request` (triggering RPM repodata generation and OCI registry push)?

## Resolution

Resolved in [docs/specs/02-nats-message-contracts.md](../../docs/specs/02-nats-message-contracts.md) and [docs/adr/0007-scheduler-dispatcher-split-and-nats-topology.md](../../docs/adr/0007-scheduler-dispatcher-split-and-nats-topology.md).
- Architectural split: `bsgo-scheduler` computes dependencies via `libsolv` and publishes to `bsgo.jobs.pending` (`WorkQueue` stream). `bsgo-dispatcher` matches jobs to worker capacity, schedules with warm-cache affinity, prioritizes critical paths, and delivers to `bsgo.workers.<worker_id>.job`.
- Subject topology:
  - `bsgo.triggers.<prj>.<pkg>` (`TRIGGERS` stream, `Limits`, 7 days)
  - `bsgo.jobs.pending` (`JOBS_PENDING` stream, `WorkQueue`)
  - `bsgo.status` (`JOB_STATUS` stream, `Limits`, 30 days)
  - `bsgo.publish.<type>.<prj>.<target>.<arch>` (`PUBLISH` stream, `WorkQueue`)
  - `bsgo.workers.heartbeat` (Core NATS ephemeral telemetry)
  - `bsgo.jobs.<job_id>.control` (Core NATS abort command for superseded builds)
  - `bsgo.logs.<job_id>` (Core NATS zero-overhead live log streaming)
- Schemas: Defined strictly in Protocol Buffers (`syntax = "proto3"`), with ProtoJSON transcoding for external HTTP webhooks and CLI tools.
- Out of scope: Early preemption (killing young builds for priority jobs) is explicitly deferred out of scope. Superseded commit cancellation is supported.
