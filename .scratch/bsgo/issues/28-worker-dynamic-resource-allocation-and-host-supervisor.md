# 28: Worker: Dynamic Multi-Job Host Supervision & Resource Bin-Packing

**What to build:**
Transform `bsgo-worker` from a single-job static daemon into a multi-job host supervisor that dynamically manages host hardware capacity (ADR 0009):
1. **Dynamic Host Pool Tracking**:
   - `WorkerDaemon` discovers or is configured with total host capacity (`TotalMemoryMB`, `TotalVCPUs`, `AvailableDiskGB`).
   - Maintains real-time accounting of allocated vs available memory and vCPUs across actively executing build jobs.
2. **Capacity Heartbeats & Multi-Job Advertising**:
   - Extend `v1.WorkerHeartbeat` and `v1.WorkerHardware` to communicate available allocatable resources (`AvailableMemoryMB`, `AvailableVCPUs`) and active job lists rather than a binary `IDLE`/`BUSY` state.
3. **Dispatcher Multi-Job Capacity Matching**:
   - Update `WorkerRegistry` in `pkg/dispatcher` to treat workers as capacity pools, matching incoming jobs whenever `AvailableMemoryMB >= req.MemoryMB` and `AvailableVCPUs >= req.VCPUs`.
   - Update worker selection to prioritize warm-cache affinity among nodes with sufficient allocatable capacity.
4. **Dynamic Concurrent KVM Micro-VMs**:
   - `bsgo-worker` concurrently executes jobs up to the host's resource ceiling, passing the exact manifest-requested memory (`--memory <MB>`) and vCPUs (`--jobs <N>`) to `/usr/bin/build`.
   - Releases allocated memory and vCPU credits back to the host pool immediately upon build completion or abort signals.

**Blocked by:** 27: Worker: Ephemeral Storage (tmpfs/RAM Disk) & KVM Performance Hardening

**Status:** resolved

- [x] Add dynamic resource budgeting in `pkg/worker/worker.go` tracking real-time available memory and vCPUs.
- [x] Update `v1.WorkerHardware` to include `AvailableMemoryMB` and `AvailableVCPUs` in heartbeats.
- [x] Update `WorkerRegistry` and `matchesConstraints` in `pkg/dispatcher` to match jobs against allocatable capacity.
- [x] Support concurrent execution of multiple isolated KVM micro-VMs in `pkg/worker/sandbox.go`.
- [x] Implement atomic credit recovery returning RAM/vCPU allocations upon job termination or failure.
- [x] Unit and integration tests verifying concurrent bin-packed execution of heterogeneous jobs (e.g. concurrent 2GB and 16GB jobs on single host supervisor).
