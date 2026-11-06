# Research Report: obs-build Worker Execution Contract (Ticket 02)

## Executive Summary

The openSUSE `/usr/lib/build/build` script (part of `obs-build`) is the core execution engine of openSUSE build workers. It is responsible for setting up the sterile build chroot/VM, installing prerequisite packages from pre-resolved lists, executing the build recipe (RPM `.spec`, Debian `rules`, Kiwi XML, Dockerfile), and extracting output artifacts and build metrics.

For `bsgo`, `bsgo-worker` acts as the host supervisor that provisions the QEMU/KVM VM overlay, prepares the input directory layout, invokes `/usr/lib/build/build`, and harvests the results.

---

## 1. Command-Line Invocation Contract

Inside the worker host/VM supervisor, `/usr/lib/build/build` is invoked with the following core arguments:

```bash
/usr/lib/build/build \
  --root /path/to/buildroot/.mount \
  --vm-type kvm \
  --vm-disk /path/to/vm_root.qcow2 \
  --vm-swap /path/to/vm_swap \
  --memory 4096 \
  --threads 4 \
  --jobs 4 \
  --arch x86_64 \
  --dist /path/to/buildroot/.build.config \
  --rpmlist /path/to/buildroot/.build.rpmlist \
  --logfile /path/to/buildroot/.build.log \
  --clean \
  --changelog \
  --norootforbuild \
  --reason "Building <package> for project <proj> arch <arch> commit <sha>" \
  /path/to/sources/<recipe_file>
```

### Key Parameter Reference
- **`--root <dir>`**: Target directory where the VM root filesystem is mounted during setup.
- **`--vm-type <type>`**: Virtualization engine (`kvm`, `qemu`, `xen`, `zvm`, `chroot`). For `bsgo`, `kvm` is standard on x86_64/aarch64/ppc64le, and `zvm` on s390x.
- **`--vm-disk <path>`**: Path to the disposable copy-on-write QCOW2 disk image.
- **`--vm-swap <path>`**: Path to dedicated swap device.
- **`--memory <MB>`**: Memory allocation for the VM.
- **`--dist <file>`**: Path to the distribution build configuration (`.build.config`), specifying macro definitions, pre-install lists, and build flags.
- **`--rpmlist <file>`**: Crucial: the deterministic list of pre-calculated RPMs downloaded from S3 to install into the chroot before compiling.
- **`--logfile <file>`**: Path where standard output and standard error from the build process are written.
- **`--norootforbuild`**: Drops root privileges inside the chroot, running the build phase as user `abuild`.
- **`--clean`**: Ensures the target build directory is completely wiped before starting.

---

## 2. Image Build Modes (Kiwi, Docker, Product Compose)

`/usr/lib/build/build` adjusts its behavior based on the recipe file passed:

- **RPM Package Builds**: Argument is `path/to/package.spec`. Produces RPM binaries and debuginfo packages.
- **OCI Container Builds (`Dockerfile` or `Containerfile`)**: Produces container image tarballs in the `DOCKER/` directory.
- **Kiwi Appliance Builds (`config.xml` or `*.kiwi`)**:
  - Requires `--kiwi-parameter --ignore-repos` and explicit `--kiwi-parameter --add-repo dir://./repos/...` pointing to local S3-synced RPM directories.
  - Outputs disk images (ISO, raw, qcow2) to the `KIWI/` directory.
- **Product Compose (`productcompose`)**: Outputs installation media manifests and repos into `PRODUCT/`.

---

## 3. Input Directory Layout Expected by Worker

Before invoking `build`, `bsgo-worker` prepares the following directory tree:

```
/var/run/bsgo-worker/jobs/<job-id>/
├── sources/
│   ├── package.spec         <- Checked out from Git
│   ├── source-tarball.tar.gz <- Fetched from Git LFS (S3)
│   └── patches...
├── .build.config            <- Distribution macros and flags
├── .build.rpmlist           <- Pre-computed list of RPM filenames
├── packages/                <- Pre-downloaded RPMs from S3 needed for chroot
├── .mount/                  <- Temporary mount point for QEMU disk
└── vm_root.qcow2            <- QCOW2 overlay on top of base image
```

---

## 4. Output Artifacts Layout

Upon completion, `build` writes all artifacts into `$buildroot/.build.packages` (or mounts it out of the VM):

```
.build.packages/
├── package-1.0-1.x86_64.rpm
├── package-devel-1.0-1.x86_64.rpm
├── package-debuginfo-1.0-1.x86_64.rpm
├── same_result_marker       <- Generated if build output is bit-for-bit identical
├── DOCKER/                  <- Container images (if container build)
├── KIWI/                    <- Appliance images (if Kiwi build)
├── PRODUCT/                 <- Product media (if Product Compose)
└── OTHER/
    ├── _buildenv            <- Exact package versions installed in chroot
    └── _statistics          <- Detailed performance timings
```

### Statistics File (`OTHER/_statistics`)
Contains exact timing breakdowns parsed by the worker:
- `TIME_preinstall`: Bootstrapping base chroot
- `TIME_install`: Installing build dependencies
- `TIME_main_build`: Compiling the software (`%build` & `%install`)
- `TIME_rpmlint`: Running packaging lint checks
- `TIME_cpusteal`: CPU contention / steal time on hypervisor

---

## 5. Exit Codes & Error Signaling

The exit code of `/usr/lib/build/build` dictates the next action in `bsgo`:

| Exit Code | OBS Meaning | `bsgo` Worker Action |
| :--- | :--- | :--- |
| **0** | **Success** | Upload built RPMs/images and logs to S3. Notify Scheduler via NATS `jobs.status` (succeeded). |
| **1** | **Build Failure** | Compilation or test failure (`%build` or `%check`). Upload build log to S3. Notify Scheduler (failed). |
| **2** | **Unchanged** | Generated identical binaries (`same_result_marker`). Mark package unchanged, avoid redundant repository publishing. |
| **3** | **Preinstall Error** | Broken chroot or missing prerequisite package. Log error, notify Scheduler. |
| **4** | **Out of Resources** | VM OOM or disk full. Flag job for retry with higher resource limits or mark worker unhealthy. |
| **9** | **Dynamic BuildRequires Changed** | The spec file emitted `%generate_buildrequires` which altered dependencies. Scheduler must re-run solver. |
