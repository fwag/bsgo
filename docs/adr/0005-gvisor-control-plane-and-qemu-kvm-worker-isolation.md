# 0005: gVisor Control Plane and QEMU/KVM Worker Isolation

We decided to run Kubernetes control plane pods under gVisor (`runtimeClassName: gvisor`) for defense-in-depth, while build workers execute untrusted compilation payloads inside sterile QEMU/KVM virtual machines with disposable QCOW2 copy-on-write overlays.

This secures the multitenant Kubernetes control plane against remote code execution while ensuring untrusted build scripts have access to the full, uncompromised Linux kernel features (chroots, loop mounts, namespaces) required by `obs-build`.
