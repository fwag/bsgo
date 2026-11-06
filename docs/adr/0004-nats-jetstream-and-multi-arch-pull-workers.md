# 0004: NATS JetStream and Pull-Based Multi-Architecture Worker Daemons

We decided to use NATS JetStream as the durable message bus, dispatching jobs to architecture-specific streams (`jobs.x86_64`, `jobs.s390x`, `jobs.ppc64le`, `jobs.aarch64`) pulled by a standalone Go daemon (`bsgo-worker`).

This enables `bsgo` to control diverse hardware architectures (including mainframes and specialized bare metal) without forcing Kubernetes or KubeVirt onto incompatible hardware, while maintaining at-least-once job delivery, worker health monitoring, and lock-free concurrency.
