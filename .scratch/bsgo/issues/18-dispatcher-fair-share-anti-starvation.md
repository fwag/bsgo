# 18: Dispatcher: Fair-Share Load Balancing & Staleness Anti-Starvation

**What to build:**
Implement fair-share scheduling and staleness aging in `bsgo-dispatcher`. Replace the static global priority queue with a fair-share load balancer that tracks moving wall-clock build time per project using exponential time decay ($Load = Load \cdot e^{-\lambda \Delta t} + t_{build}$). Add staleness aging bonuses to long-waiting pending jobs to ensure background builds are never starved under sustained interactive workloads.

**Blocked by:** 16: Scheduler: Rebuild Policies, Prioritized Ingestion & Admin Trigger Tool

**Status:** closed

- [x] Project-level exponential decay build time tracking in dispatcher.
- [x] Fair-share deficit round-robin scheduling across competing projects.
- [x] Staleness aging bonus dynamically boosting long-waiting pending jobs.
- [x] Source-change bias prioritizing human/interactive commits over automated background rebuilds.
