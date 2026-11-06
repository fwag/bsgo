# 12: Worker: bsgo-worker Execution Sandbox & S3 Upload

**What to build:**
Implement the standalone `bsgo-worker` daemon running on target hardware (x86_64, aarch64, s390x, ppc64le). The worker connects to NATS, emits periodic heartbeats, and receives a `BuildJob` on its private inbox. It prepares the input layout (`.build.rpmlist`, `.build.config`), downloads pre-resolved chroot RPMs from S3/Garage, creates a sterile QCOW2 overlay on a pristine base image, and executes `/usr/lib/build/build` inside QEMU/KVM. It streams live stdout/stderr log chunks over Core NATS (`bsgo.logs.<job_id>`), harvests built RPMs, logs, and `OTHER/_statistics`, uploads them directly to S3/Garage, and publishes `JobStatusUpdate` to `bsgo.status`.

**Blocked by:** 11: Dispatcher: Priority Matching, Warm-Cache Affinity, and Job Leasing

**Status:** ready-for-agent

- [ ] Worker daemon connecting to NATS, emitting heartbeats, and listening on `bsgo.workers.<worker_id>.job`.
- [ ] Chroot RPM downloader fetching pre-resolved binaries from S3/Garage into local worker SSD cache.
- [ ] QCOW2 copy-on-write overlay manager creating disposable VM disks on top of pristine base images.
- [ ] Process executor invoking `/usr/lib/build/build` with standardized CLI flags and exit code translation (0, 1, 2, 3, 4, 9).
- [ ] Live log streamer publishing stdout/stderr chunks to ephemeral Core NATS subject `bsgo.logs.<job_id>`.
- [ ] Direct S3/Garage artifact uploader uploading completed RPMs, build logs, and statistics, publishing `JobStatusUpdate`.
- [ ] Abort listener handling `bsgo.jobs.<job_id>.control` to immediately kill the build process and recycle the VM disk.
- [ ] Integration tests verifying worker execution, log streaming, and artifact upload using local test chroots and Garage S3.
