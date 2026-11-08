# 27: Worker: Ephemeral Storage (tmpfs/RAM Disk) & KVM Performance Hardening

**What to build:**
Align `bsgo-worker` with production Open Build Service Level 4 worker specifications (modeled after SUSE production fleet `h01-ch3b`):
1. **Configurable Ephemeral Workspace**:
   - Support `tmpfs` / RAM-disk mode (`/dev/shm` or dynamic mount) for memory-rich cloud nodes, guaranteeing zero residual compilation state on physical storage.
   - Support dedicated fast NVMe disk partitions with separated control state in `/run/bsgo/worker/` (on `tmpfs`) and workspace in `/var/cache/bsgo/worker/`.
2. **KVM Virtualization Tuning (Parity with h01-ch3b)**:
   - Configure QEMU virtual disk storage with `cache=unsafe,aio=io_uring` to eliminate guest `fsync()` write bottlenecks and match ramdisk I/O throughput.
   - Enforce strict network air-gapping (`-net none`) during compilation execution to guarantee reproducible and hermetic builds.
   - Run hypervisor with unprivileged user separation (`-run-with user=qemu`) and QEMU seccomp filters (`-sandbox on`).
3. **Zero-Residual Teardown**:
   - Implement deterministic post-build wiping of sparse VM disk files (`build-root.img`, `build-root.swap`) on both success and abort signals to prevent disk leakage and cross-job contamination.

**Blocked by:** 12: Worker: bsgo-worker Execution Sandbox & S3 Upload

**Status:** open

- [ ] Support `tmpfs` / ramdisk path configuration in `config/worker-kvm.yaml` (`work_dir: /dev/shm/...`).
- [ ] Separate volatile control plane state (`statedir` in `/run`) from build overlays in `bsgo-worker`.
- [ ] Pass `cache=unsafe,aio=io_uring` to QEMU / `obs-build` invocation in `pkg/worker/sandbox.go`.
- [ ] Enforce `-net none` and unprivileged execution (`-run-with user=qemu` / `-sandbox on`) in KVM mode.
- [ ] Implement deterministic cleanup routine wiping VM disk overlays and swap images upon job completion or termination.
- [ ] Integration tests verifying clean teardown, zero residual state, and I/O performance on tmpfs vs NVMe.
