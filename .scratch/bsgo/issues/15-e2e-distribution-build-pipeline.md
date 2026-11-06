# 15: E2E: Full Distribution Build Pipeline & Live Charm TUI Dashboard

**What to build:**
Deliver the final end-to-end integration and verification of the `bsgo` distribution build pipeline. Wire the complete flow: Git push to bare test repo $\to$ API webhook ingestion $\to$ Scheduler in-process `libsolv` dependency solving $\to$ Dispatcher critical-path queuing $\to$ Worker QEMU compilation and live log streaming $\to$ Publisher incremental S3 repodata generation $\to$ repository consumption by `zypper`/`dnf`. Deliver the fully operational interactive Charm `bubbletea` TUI dashboard (`bsgo watch`) showing live build jobs, worker capacity, and log drawer.

**Blocked by:** 12: Worker: bsgo-worker Execution Sandbox & S3 Upload, 13: Publisher: Incremental S3 Repodata Generation & Atomic Publication, 14: Security: Authentik OIDC, JIT User Provisioning, and Scoped Agent Tokens

**Status:** ready-for-agent

- [ ] End-to-end integration test (`test/e2e`) running in Podman with PostgreSQL, NATS JetStream, and Garage S3.
- [ ] Automated build verification compiling a real RPM package from a local bare Git repository through the full stack.
- [ ] Published repository validation verifying generated `repomd.xml` and package installation via standard package managers.
- [ ] Fully connected Charm `bubbletea` live dashboard (`bsgo watch`) streaming live logs from NATS and showing real worker fleet utilization.
- [ ] Production Kubernetes deployment manifests with gVisor sandboxing (`runtimeClassName: gvisor`) and bare-metal worker systemd units.
