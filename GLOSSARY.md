# Build Service Go (bsgo)

A cloud-native, Git-backed reimplementation of the Open Build Service backend in Go, designed for Kubernetes.

## Language

**Project**:
A logical grouping of packages, build targets, and access policies represented by a Git repository containing project manifests.
_Avoid_: Build project, namespace, collection

**Package**:
An independently version-controlled software component living in its own Git repository containing recipes, sources, and patches.
_Avoid_: Module, component, subproject

**Project Manifest**:
A declarative configuration tracking which packages, repository references, and revisions belong to a Project, sharded by consistent hash to balance storage and tree sizes.
_Avoid_: Submodule index, project meta, package list

**Manifest Shard**:
A sub-directory path derived from a hash of the package name (e.g. `packages/e4/`) ensuring even distribution across thousands of packages.
_Avoid_: Bucket, directory partition

**Build Target**:
A build architecture and base dependency repository definition against which a Package is compiled.
_Avoid_: Build repo, architecture target, distro definition

**Build Configuration**:
The rules that govern dependency resolution and build execution for a Build Target (preferences, ignores, preinstall set, release scheme, defines, rpm macros), declared in the Project Manifest and equivalent to an OBS prjconf.
_Avoid_: prjconf, _config, project config, meta config

**Config Scope**:
The visibility of a Build Configuration section: public (every Consumer Project), protected (this Project and its subprojects), or private (this Project only).
_Avoid_: Namespace-scoped config, inheritance level

**Consumer Project**:
The Project whose build is being configured when Build Configurations from several Projects along a dependency path are layered; it determines which Config Scopes apply.
_Avoid_: Viewpoint, child project, downstream project

**Bootstrap Dependency**:
A dependency entry added to a Build Target only until every Package has been built once against it, then removed so the Project rebuilds against its own binaries.
_Avoid_: Seed repo, base repo, temporary path

**Build Job**:
A unit of work dispatched to a worker to build a specific package revision against a build target (supports RPM, OCI Container, and Kiwi images).
_Avoid_: Task, build step, compilation run

**Worker**:
An isolated execution agent running on target architecture hardware (x86_64, aarch64, s390x, ppc64le) that pulls build jobs from architecture-specific queues and runs the build toolchain inside sterile VMs.
_Avoid_: Build slave, builder daemon, build minion

**Scheduler**:
The control plane service responsible for monitoring package changes, solving dependency trees with libsolv, and producing deterministic build jobs.
_Avoid_: Dispatcher, build queue manager

**Dispatcher**:
The control plane service responsible for tracking worker capacity, evaluating hardware constraints, matching jobs to workers, and managing execution leases.
_Avoid_: Build queue, worker manager

**Artifact Store**:
An S3-compatible object store housing compiled binaries, RPM repodata, OCI layers, and build logs.
_Avoid_: Binary cache, repo storage, pool

**Operational Projection**:
A high-speed relational representation in PostgreSQL projected from Git project repositories, used by the scheduler to query manifests without Git I/O overhead.
_Avoid_: Shadow database, sync copy

**Published Repository**:
A consumable software repository (RPM YUM/DNF repository or OCI image registry) exposed to consumers, backed by the Artifact Store.
_Avoid_: Release pool, mirror, export dir

**Superuser**:
A cluster administrator with universal permissions across all projects, workers, and system configurations.
_Avoid_: Root, system admin, global operator

**Project Maintainer**:
A user or group granted full administrative and operational control over a specific Project.
_Avoid_: Project admin, project owner, project lead
