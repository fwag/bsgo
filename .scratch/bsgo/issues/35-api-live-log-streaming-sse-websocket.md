# 35: API Gateway: Live Log Streaming Endpoint via SSE / WebSocket

**What to build:**
Expose an authenticated streaming endpoint on the HTTP API gateway connecting client frontends to NATS live build logs:
1. **Server-Sent Events (SSE) / WebSocket Route**: Add `GET /api/v1/jobs/{job_id}/logs` supporting `text/event-stream` (or WebSocket) to stream real-time build output from `bsgo.logs.<job_id>`.
2. **Historical Log Replay**: If the job has completed, stream the archived build log from S3; if running, stream the NATS JetStream/Core NATS tail and forward incoming lines.
3. **CLI Integration**: Wire `bsgo package watch` and `bsgo job logs <job_id>` directly to this gateway endpoint.

**Blocked by:** 08: Foundation: Project and Package Ingestion Gateway with GitOps Bridge

**Status:** resolved

- [x] Implement SSE / WebSocket handler in `pkg/gateway` bridging to NATS `bsgo.logs.<job_id>`.
- [x] Implement S3 log archive fallback for completed builds.
- [x] Connect CLI `watch` command to streaming API.
- [x] Integration test streaming live logs across NATS to HTTP client.
