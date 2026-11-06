# Issue 02: Research obs-build Worker Execution Contract

Status: closed
Labels: `wayfinder:research`

## Question

What exact CLI contract, parameters, input directory layout, and environment variables does the openSUSE `/usr/lib/build/build` script expect inside a QEMU/KVM VM when invoked for RPM, Kiwi image, and OCI container builds, and what are its exact exit codes and output artifacts?

## Resolution

Resolved in [docs/research/02-obs-build-worker.md](../../docs/research/02-obs-build-worker.md).
- `/usr/lib/build/build` accepts `--root`, `--vm-type kvm`, `--dist`, `--rpmlist`, and recipe files.
- Supports RPM, Kiwi appliances (`--kiwi-parameter --ignore-repos`), and OCI Docker containers.
- Inputs: `.build.config`, `.build.rpmlist`, pre-downloaded RPMs from S3.
- Outputs: `.build.packages/` (RPMs, `DOCKER/`, `KIWI/`, `OTHER/_statistics`).
- Exit codes: 0 = success, 1 = failure, 2 = unchanged, 3 = preinstall/deps error, 4 = OOM/resources, 9 = dynamic BuildRequires changed.
