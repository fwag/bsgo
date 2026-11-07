# 19: Dispatcher: Worker Fault Tolerance, badhost Tracking & Quarantine

**What to build:**
Implement worker fault isolation and health tracking in `bsgo-dispatcher`. Track worker failure history per job (`badhost`) to avoid re-assigning a failed job back to a worker with host-level environment corruption. Implement an automated circuit breaker / quarantine state for workers reporting consecutive infrastructure faults (KVM module faults, disk full, permission errors).

**Blocked by:** 18: Dispatcher: Fair-Share Load Balancing & Staleness Anti-Starvation

**Status:** closed

- [x] `badhost` tracking in worker registry associating failed `(job_id, worker_id)`.
- [x] Dispatcher filtering ensuring retried jobs bypass previously failed workers.
- [x] Failure classification distinguishing package build failures (exit 1) from infrastructure host failures (exit 2/3/9).
- [x] Automated worker quarantine and health recovery transitions.
