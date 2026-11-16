# Specification: NATS JetStream Message Contracts and Topology

This specification defines the NATS JetStream subject hierarchy, stream retention policies, and Protocol Buffer message schemas governing communication between the API, Scheduler, Dispatcher, Workers, and Publisher.

---

## 1. Subject Hierarchy and Topology

The communication fabric uses a combination of **persistent JetStream streams** (for guaranteed at-least-once delivery of jobs and events) and **ephemeral Core NATS pub/sub** (for zero-overhead telemetry and streaming).

```
bsgo.
├── triggers.
│   └── <project>.<package>            [JetStream: TRIGGERS] Webhooks / manual build triggers
├── jobs.
│   ├── pending                        [JetStream: JOBS_PENDING] Solved jobs awaiting worker assignment
│   ├── status                         [JetStream: JOB_STATUS] Job lifecycle transitions & outcomes
│   └── <job_id>.
│       └── control                    [Core NATS] Cancellation / abort commands to active worker
├── workers.
│   ├── heartbeat                      [Core NATS] Periodic worker health, capacity & cache state
│   └── <worker_id>.
│       └── job                        [Core NATS] Direct job assignment from Dispatcher
├── logs.
│   └── <job_id>                       [Core NATS] Live log line streaming
├── signing.
│   ├── finished                       [JetStream: SIGN_REQUESTS] Job built, RPMs to sign (job-finished)
│   └── signed                         [JetStream: SIGN_RESULTS] Signed artifacts or signing failure (job-signed)
└── events.
    ├── repo.changed                   [Core NATS] Build repository of a prpa changed (indexer -> checker, publisher)
    └── published                      [Core NATS] A prpa was published (publisher)
```

---

## 2. Stream Configurations & Retention Policies

| Stream Name | Subjects | Retention Policy | Storage | AckWait | MaxAge | Description |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **`TRIGGERS`** | `bsgo.triggers.>` | `Limits` | File | — | 7 Days | Immutable log of all inbound build trigger requests. |
| **`JOBS_PENDING`** | `bsgo.jobs.pending` | `WorkQueue` | File | 60s | 14 Days | Schedulers publish resolved jobs. Dispatcher consumes and assigns to workers. |
| **`JOB_STATUS`** | `bsgo.status` | `Limits` | File | — | 30 Days | Durable audit trail of job state transitions (`RUNNING`, `FAILED`, `SUCCEEDED`). |

*Note: `bsgo.workers.heartbeat`, `bsgo.logs.*`, and `bsgo.jobs.*.control` do NOT use JetStream streams; they operate on standard Core NATS to eliminate storage and I/O overhead.*

---

## 3. Protocol Buffer Schemas

### 3.1. Build Trigger: `bsgo.triggers.<project>.<package>`
Emitted by the API service upon receiving a Git webhook or CLI trigger.

```protobuf
syntax = "proto3";
package bsgo.v1;

message BuildTrigger {
  string trigger_id = 1;      // UUID
  string project = 2;         // e.g. "openSUSE:Factory"
  string package = 3;         // e.g. "chromium"
  string source_git_url = 4;
  string commit_sha = 5;      // Exact commit SHA
  string ref = 6;             // e.g. "refs/heads/main"
  string triggered_by = 7;    // User or service identifier
  int64 timestamp_unix = 8;
  bool force_rebuild = 9;     // Bypass identical build check
}
```

### 3.2. Deterministic Build Job: `bsgo.jobs.pending` & `bsgo.workers.<worker_id>.job`
Emitted by `bsgo-scheduler` after dependency resolution with `libsolv`.

```protobuf
syntax = "proto3";
package bsgo.v1;

message BuildJob {
  string job_id = 1;          // UUID
  string project = 2;
  string package = 3;
  string target = 4;          // e.g. "standard"
  string architecture = 5;    // e.g. "x86_64", "aarch64", "s390x"
  int32 priority = 6;         // Higher value = higher priority (topological depth)

  SourceSpec source = 7;
  repeated ChrootRPM chroot_rpms = 8;
  ResourceRequirements resources = 9;
  ArtifactDestination destination = 10;
  int64 created_at_unix = 11;

  // Pre-resolved build contract (WP5). When set, the worker runs hermetically:
  // it fetches exactly these bdeps, passes --rpmlist and no --repo, and checks
  // out exactly source_commit. When absent the worker falls back to the legacy
  // fields (chroot_rpms, repositories, build_config, ...) and logs
  // "legacy job without buildinfo" on stderr.
  BuildInfo buildinfo = 12;

  // Resolved build constraints (OBS-compatible, see below). Absent = none.
  JobConstraints constraints = 13;
}

message JobConstraints {
  repeated SandboxConstraint sandbox = 1;  // all must hold
  int64 memory_mb = 2;        // informational, already folded into resources.memory_mb
  int64 disk_mb = 3;          // informational, already folded into resources.disk_gb
  int32 processors = 4;       // informational, already folded into resources.vcpus
}

message SandboxConstraint {
  string value = 1;           // "kvm", "nspawn", "chroot", "xen", "zvm", or "secure"
  bool exclude = 2;           // true: the worker sandbox must NOT be value
}

message BuildInfo {
  string config = 1;          // worker variant of the layered prjconf -> .build.config, --dist
  repeated Bdep bdeps = 2;    // ordered, fully expanded
  string archpath = 3;        // --arch, e.g. "x86_64:i686:i586:noarch" (scheduler-provided)
  string release = 4;         // --release
  string disturl = 5;         // --disturl
  string flavor = 6;          // --buildflavor (multibuild)
  string obspackage = 7;      // --obspackage
  string source_commit = 8;   // full commit id, fetched with "fetch --depth 1 origin <id>"
  string meta = 9;            // echoed back in the job-finished message
  int32 logidlelimit = 10;    // seconds without output before ABORTED; 0 = off (worker enforced)
  int32 jobs = 11;            // --jobs; 0 = Resources.vcpus
}

message Bdep {
  string name = 1;
  string epoch = 2;
  string version = 3;
  string release = 4;
  string arch = 5;
  string sha256 = 6;          // binary identity, mandatory; mismatch fails the job
  int64 size = 7;
  BdepSource source = 8;      // exactly one of s3_key / url
  bool preinstall = 9;        // rpmlist "preinstall:" line
  bool vminstall = 10;        // rpmlist "vminstall:" line
  bool runscripts = 11;       // rpmlist "runscripts:" line
  bool noinstall = 12;        // rpmlist "noinstall:" line
  bool installonly = 13;      // rpmlist "installonly:" line
  bool notmeta = 14;          // meta digest only, not in the rpmlist
}

message BdepSource {
  string s3_key = 1;          // own repositories, artifact bucket
  string url = 2;             // DoD: absolute http(s) URL = rpm-md base URL + location href
}
```

The legacy fields `chroot_rpms`, `repositories`, `build_config`, `macros`,
`build_flags`, `prefer`, `preinstall`, `vminstall`, `support`, `required` and
`order` are superseded by `buildinfo` and ignored whenever it is present.

BuildInfo jobs are refused in `obs-build` (plain chroot) sandbox mode unless
the worker sets `sandbox.allow_unisolated_chroot: true`, because obs-build
chroot mode has no network isolation. KVM mode runs with `-net none`.

#### Build constraints (`constraints`)

The scheduler resolves the constraints of every job like OBS `bs_dispatch` and
puts the dispatch relevant part into `constraints`. Sources, lowest to highest
precedence:

1. the project configuration `Constraint:` lines (`sandbox kvm`,
   `hardware:memory:size unit=G 16`, ...);
2. the package `_constraints` file (XML, `<sandbox>`, `<hardware>`, ...) after
   applying its `<overwrite><conditions>` sections: an `<arch>` list must
   contain the build architecture, a `<package>` list the package name or its
   short name (cut at the first `.`); the overwrite replaces list constraints
   instead of extending them;
3. the recipe `#!BuildConstraint:` lines;
4. the bsgo-native override `build.constraints.sandbox` of the package manifest,
   which replaces the sandbox constraint entirely (`none` removes it).

Merging 1-3 follows `mergeconstraints`: list constraints (`sandbox`,
`hostlabel`, cpu flags) accumulate, scalar constraints (processors, memory,
disk, ...) are replaced by the higher source.

Enforced: `sandbox` (equal to the worker's `sandbox_type`; `secure` accepts
`kvm`, `xen`, `zvm`; `exclude` negates), and `hardware:memory`,
`hardware:disk`, `hardware:processors`, which raise the job's `resources`
(never lower them) so the existing capacity matching and VM sizing apply.
Parsed and merged but not enforced (the workers do not report the facts):
`hostlabel`, `linux`, `hardware:jobs`, `memoryperjob`, `physicalmemory`, cpu
`flag`. Malformed constraints put the unit in state `broken`.

A job whose `constraints.sandbox` is empty is unconstrained and may run on any
worker, except workers that set `constrained_only` in their heartbeat. A job
with a sandbox constraint stays queued until a matching worker reports; the
package status detail reads for example `new build, needs sandbox kvm`.

```protobuf

message SourceSpec {
  string git_url = 1;
  string commit_sha = 2;
  string recipe_path = 3;     // Path to .spec, Dockerfile, or config.xml
}

message ChrootRPM {
  string filename = 1;        // e.g. "glibc-devel-2.38-1.x86_64.rpm"
  string s3_key = 2;          // S3 object key or DOD URL
  string sha256 = 3;          // Verification checksum
  int64 size_bytes = 4;
}

message ResourceRequirements {
  int32 vcpus = 1;            // e.g. 4
  int64 memory_mb = 2;        // e.g. 8192
  int64 disk_gb = 3;          // e.g. 50
  int32 timeout_seconds = 4;  // e.g. 14400 (4h)
  string sandbox_type = 5;    // legacy, unused; see JobConstraints
}

message ArtifactDestination {
  string s3_bucket = 1;
  string s3_prefix = 2;       // Target directory prefix in S3
}
```

### 3.3. Worker Heartbeat: `bsgo.workers.heartbeat`
Emitted every 10–15 seconds by `bsgo-worker` to inform the Dispatcher of capacity.

```protobuf
syntax = "proto3";
package bsgo.v1;

enum WorkerStatus {
  WORKER_STATUS_UNSPECIFIED = 0;
  WORKER_STATUS_IDLE = 1;
  WORKER_STATUS_BUSY = 2;
  WORKER_STATUS_DRAINING = 3;
}

message WorkerHeartbeat {
  string worker_id = 1;       // Unique worker host identifier
  string architecture = 2;    // Host architecture
  repeated string archs = 8;  // Architectures the worker can build (see below)
  WorkerStatus status = 3;
  string active_job_id = 4;   // Present if status == WORKER_STATUS_BUSY
  
  WorkerHardware hardware = 5;
  repeated string cached_rpm_hashes = 6; // Hashes present in local worker SSD cache
  int64 timestamp_unix = 7;
  bool constrained_only = 9;  // take only jobs with a sandbox constraint
}

message WorkerHardware {
  int32 total_vcpus = 1;
  int64 total_memory_mb = 2;
  int64 available_disk_gb = 3;
  string sandbox_type = 4;    // "kvm", "nspawn", "chroot" (obs-build), from sandbox.mode
  repeated string cpu_features = 5; // e.g. ["avx2", "avx512"]
}
```

`sandbox_type` is derived from `sandbox.mode`: `kvm` and `nspawn` as is,
`obs-build` is advertised as `chroot` (the OBS name). `constrained_only` is the
inverse of `sandbox.accept_unconstrained` (default true): a KVM worker next to an
nspawn worker sets it to false and then only receives jobs that constrain the
sandbox.

The heartbeat doubles as registration. `archs` lists the build architectures the
worker offers: by default the BSCando set of the host architecture (an `x86_64`
host offers `x86_64`, `i586`, `i686`), or the explicit `worker.archs` list of the
worker configuration. The dispatcher assigns a job only to a worker whose `archs`
contains the job's `architecture`. A heartbeat without `archs` (older worker)
means "only `architecture`". For architectures with a personality helper in the
cando table (`i586` on `x86_64`), the worker runs obs-build under `linux32`, like
`bs_worker`; obs-build itself does not switch the personality.

### 3.4. Job Control (Superseded Abort): `bsgo.jobs.<job_id>.control`
Emitted by `bsgo-dispatcher` to terminate builds superseded by new commits.

```protobuf
syntax = "proto3";
package bsgo.v1;

enum ControlAction {
  CONTROL_ACTION_UNSPECIFIED = 0;
  CONTROL_ACTION_ABORT = 1;
  CONTROL_ACTION_DRAIN = 2;
}

message JobControl {
  string job_id = 1;
  ControlAction action = 2;
  string reason = 3;          // e.g. "SUPERSEDED_BY_COMMIT:9a2b4c"
  int64 timestamp_unix = 4;
}
```

### 3.5. Live Log Stream: `bsgo.logs.<job_id>`
Emitted by `bsgo-worker` during build execution.

```protobuf
syntax = "proto3";
package bsgo.v1;

message LogChunk {
  string job_id = 1;
  int64 sequence_nr = 2;      // Monotonically increasing chunk ID
  bytes payload = 3;          // Stdout / stderr text buffer
  bool is_stderr = 4;
  int64 timestamp_unix = 5;
}
```

### 3.6. Job Status Update: `bsgo.status`
Emitted by `bsgo-worker` upon state transitions and build completion.

```protobuf
syntax = "proto3";
package bsgo.v1;

enum JobPhase {
  JOB_PHASE_UNSPECIFIED = 0;
  JOB_PHASE_FETCHING_SOURCES = 1;
  JOB_PHASE_PREPARING_CHROOT = 2;
  JOB_PHASE_BUILDING = 3;
  JOB_PHASE_UPLOADING_ARTIFACTS = 4;
  JOB_PHASE_SUCCEEDED = 5;
  JOB_PHASE_FAILED = 6;
  JOB_PHASE_UNCHANGED = 7;
  JOB_PHASE_ABORTED = 8;
}

message JobStatusUpdate {
  string job_id = 1;
  string worker_id = 2;
  JobPhase phase = 3;
  int32 exit_code = 4;        // Exit code from obs-build (0, 1, 2, etc.)
  string error_message = 5;
  
  BuildStatistics statistics = 6;
  repeated ProducedArtifact artifacts = 7;
  int64 timestamp_unix = 8;
  string source_commit = 9;   // exact commit built (provenance); BuildInfo jobs only
  string meta = 10;           // BuildInfo.meta echoed back
  string release = 11;        // BuildInfo.release echoed back
}

message BuildStatistics {
  double preinstall_seconds = 1;
  double install_seconds = 2;
  double main_build_seconds = 3;
  double total_seconds = 4;
  int64 max_memory_bytes = 5;
  int64 cpu_user_time_ms = 6;
  int64 cpu_system_time_ms = 7;
}

message ProducedArtifact {
  string filename = 1;        // e.g. "ripgrep-14.1.0-1.x86_64.rpm"
  string s3_key = 2;
  int64 size_bytes = 3;
  string sha256 = 4;
}
```

Implementation notes (wire format of the current worker): the status message is
a JSON object on `bsgo.status` with `phase` in `SUCCEEDED | FAILED | ABORTED`,
`exit_code`, `reason`, `artifacts`, `total_seconds`, and for BuildInfo jobs
`source_commit`, `meta` and `release`. `ABORTED` (with `reason`) is reported for
the job timeout (`resources.timeout_seconds`), the log idle limit
(`buildinfo.logidlelimit`), an abort control message, and a build killed by a
signal; a build that exits non-zero is `FAILED`. For BuildInfo jobs the artifact
list holds the binaries plus `bininfo.json`, `_slsa_provenance.json`,
`build.log` and `_statistics` (when present), all uploaded under
`artifacts/<project>/<target>/<arch>/<package[:flavor]>/`, with `s3_key` set to
the real object key. The worker does not emit a publication request for
BuildInfo jobs; the indexer integrates the binaries from this message.

#### Signing stage (plan 02, S3): `bsgo.signing.finished` and `bsgo.signing.signed`
With signing on (`--sign-rpms`, default true) a `SUCCEEDED` status of a job that
uploaded RPMs does not integrate: the state handler sets the unit to `signing`
and stores the job on `bsgo.signing.finished` (job-finished). Both subjects are
JetStream work-queue streams (`SIGN_REQUESTS`, `SIGN_RESULTS`, retention
`WorkQueuePolicy`, file storage, 14 days); the job ID is the dedup message ID.
Signer replicas (`--role signer`, part of `all`) share the durable pull consumer
`bsgo-signer` (explicit ack, `AckWait` `--sign-ack-wait`, default 30 m, sized
for large RPM sets; the signer sends `InProgress` at a third of it while a job
runs; `MaxDeliver` `--sign-max-deliver`, default 5). A request is acknowledged
only after the signed objects are uploaded and the answer is stored. A request
that still fails on its last delivery gets a failure answer (`error` set), so
the unit ends `failed`; a malformed request is terminated. Both subjects carry
the same JSON object:

```json
{"job_id": "...", "project": "home:bot", "package": "foo:mini", "target": "standard",
 "architecture": "x86_64", "meta": "...", "source_commit": "...",
 "artifacts": [{"filename": "...", "s3_key": "...", "sha256": "...", "size_bytes": 0}],
 "error": ""}
```

The request lists the artifacts as the worker reported them (unsigned sha256).
The signer processes it only while the unit is in state `signing` with the same
`job_id` (a redelivered or superseded request is dropped). It downloads the RPMs
(verifying the worker's sha256), runs `rpmsign --delsign` and `--addsign` with
`bsgo-sign-helper`, verifies with `rpm -K` against the project public key,
re-extracts `bininfo.json`, uploads RPMs and `bininfo.json` over the unsigned
objects (same keys) and publishes the answer on `bsgo.signing.signed`
(job-signed): the same job with the artifact list after signing (new sha256 and
size) or, after the retries (4 attempts, backoff 2 s doubling), `error` set.
The state handlers share the durable pull consumer `bsgo-jobstatus-signed`
(unlimited redelivery with a 10 s delay, acknowledged after the integration).
The state handler consumes job-signed: unit state `signing` (or `finished`) with
a matching `job_id` becomes `finished`, the signed artifacts are integrated
(3.9) and the unit becomes `succeeded`; with `error` the unit becomes `failed`
with details `signing failed: <error>`. Answers for other jobs or for units that
are no longer `signing`/`finished` are ignored. Projects without signing (flag
off, or no key and `--sign-create-keys=false`) and jobs without RPMs skip the
stage and integrate at once. Requests and answers survive restarts and lost
connections, so no unit stays in `signing`.

Build states added by migration 00009: `signing`, `finished` and `dispatching`.
The checker treats them like `building` (jobs in flight).

Dispatch lifecycle. When the dispatcher hands a job to a worker it publishes
`bsgo.status` with phase `DISPATCHING`, which sets the unit to `dispatching`.
The worker publishes phase `BUILDING` when it actually starts the job, which sets
`building`; `updated_at` of the row is then the build start, and the elapsed time
shown by the CLI counts from it. A `DISPATCHING` message that arrives after the
worker's `BUILDING` of the same job is ignored. The orphan reconciler requeues
`dispatching` and `building` rows older than the grace period (3 minutes) that
no worker reports in its heartbeat and the dispatcher does not track, with a
`REQUEUED` message (back to `scheduled`).

#### Repository state (`prpa_state`)
Migration 00010 adds `prpa_state(project, repo, arch, state, details,
index_hash, updated_at)`, the state of the repository as a whole, after the
OBS repository states of `osc results`. `index_hash` is the content hash of the
build repository index the row refers to.

| State | Writer | Meaning |
|-------|--------|---------|
| `broken` | checker | The project configuration of the repository cannot be layered or parsed; `details` has the error. |
| `building` | checker | Jobs are in flight (`scheduled`, `dispatching`, `building`, `signing`, `finished`). |
| `blocked` | checker | No job in flight, but units are blocked (dependencies not ready, sources unavailable). |
| `finished` | checker, publisher | All work is done and the current index is not published (also after a failed publish, with `details`). |
| `publishing` | publisher | A publish is writing the repository. |
| `published` | publisher | The published repository is the one built from `index_hash`. |
| `unpublished` | checker, publisher | The target does not publish (`flags.publish` off or no rpm-md repository type). |

The checker writes the state at the end of each pass with a pure function
(`storage.DeriveRepoState`), in this precedence: configuration error (`broken`),
jobs in flight (`building`), blocked units (`blocked`), otherwise
`unpublished` or `finished`. It does not undo the publisher: `publishing`, and
`published` while `index_hash` equals the current index hash, are kept, so the
state does not flap between passes. Any new work or a changed index replaces
them. The OBS state `scheduling` (a pass is running) is not recorded.

### 3.7. Publication (no request message)
There is no publication request and no `PUBLISH` stream. The former
`bsgo.publish.>` request, which workers emitted with the list of new RPMs, is
removed: publishing is driven by the build repository index, not by workers.
The publisher reacts to the repo-changed event (3.8) and publishes the current
binary set of the prpa (see 3.9, "Publisher coordination"). After a publish it
emits `bsgo.events.published` (Core NATS, JSON `PublicationEvent`).
Deployments that still have a `PUBLISH` stream can delete it.

### 3.8. Repo Changed Event: `bsgo.events.repo.changed`
Emitted by the indexer after it replaced the binaries of a unit in the build
repository of a prpa (project, repository, architecture) and the index content
changed. Core NATS subject, not persisted. Every prpa that has the repository in
its build path (and the prpa itself) becomes dirty and is checked again.

```json
{"project": "SUSE:SLFO:1.2", "repo": "standard", "arch": "x86_64"}
```

Cross-architecture export: when the job of a unit in prpa A finishes, the
indexer splits its binaries by the layered `ExportFilter` (OBS semantics: rules
tried in reverse order of their regular expression text, first match wins; the
targets are architectures, `.` is the own architecture; a matching rule without
targets drops the binary; a binary matching no rule stays in A; an empty filter
uses the OBS default of A). Binaries targeted at another architecture B of the
same project and repository are written to the build repository of prpa B as the
import unit `<unit>:import:<A>` (for example `glibc:import:i586`), keeping the S3
key of the original artifact. Each export replaces the previous import set of
that name, also with an empty set, and B gets its own repo-changed event. Import
units are not buildable, have no artifact prefix, and are never re-exported.

### 3.9. Scheduler coordination (NATS KV)
Server replicas share the scheduler work by prpa through two JetStream KV
buckets. Keys are the unpadded base64url encoding of the prpa string
`project/repo/arch`.

| Bucket | Content | Lifetime |
|--------|---------|----------|
| `bsgo-checker-dirty` | `{"prpa","first","last"}` (unix nanoseconds of the first and the latest mark). Written by package, project config, repo-changed and job-finished events and by the periodic resync. | Until a pass consumed it; `Clear` is a compare-and-delete on the revision, so a mark refreshed during a pass survives. |
| `bsgo-checker-leases` | Owner id of the replica that checks the prpa (key `<prpa>`), or writes its index (key `index/<prpa>`). | Bucket TTL (30 s), renewed every 10 s while the pass runs. |

A replica processes a dirty prpa only when its mark is older than the debounce
interval (or the first mark is older than ten intervals) and it holds the lease.
The build triggers on `bsgo.triggers.>` no longer create jobs: they mark the
project's prpas dirty, and a trigger with `force_rebuild` first records a forced
rebuild request in PostgreSQL that the checker consumes when it schedules the
unit. `bsgo.status` messages of phase `BUILDING`, `SUCCEEDED`, `FAILED` and
`ABORTED` update the unit's build state; a `SUCCEEDED` message first integrates
the reported artifacts (`bininfo.json` plus RPMs, verified by sha256) into the
build repository. `REQUEUED` (published by the dispatcher watchdog with
`reason: WORKER_LEASE_EXPIRED` when it puts a job of a lost worker back into its
queue) returns the unit to `scheduled`; worker loss is not a build failure.

#### Publisher coordination
Publisher replicas use two further KV buckets with the same key encoding and
semantics as the checker buckets, so that publishing and checking never share
marks or leases.

| Bucket | Content | Lifetime |
|--------|---------|----------|
| `bsgo-publisher-dirty` | Same value as `bsgo-checker-dirty`. Written by the repo-changed event (3.8) and once per replica at startup for every target with `flags.publish`. | Until a publish consumed it. |
| `bsgo-publisher-leases` | Owner id of the replica that publishes the prpa (key `<prpa>`). | Bucket TTL (30 s), renewed every 10 s while the publish runs. |

A replica publishes a dirty prpa after the debounce interval
(`--publisher-debounce`, default 10 s) and only while it holds the lease, so two
replicas never publish the same prpa concurrently. A failed publish keeps the
mark and is retried. A publish reads the current binaries from the build
repository index, writes only to the published prefix `repos/<project>/<repo>/<arch>/`
(debug packages to `repos/<project>/<repo>/debug/<arch>/` when `flags.debuginfo`
is set) and ends with the upload of `repodata/repomd.xml`; objects of binaries
that are no longer published are deleted after that upload.

Repository metadata signing (plan 02, S2; `--sign-repos`, default true): the
publisher signs `repomd.xml` through bsgo-signd with the project key (inherited
from the parent namespace; created on first use unless `--sign-create-keys=false`,
in which case the project is published unsigned and a log line says so once).
Upload order per repository: data files (`repodata/*`), then
`repodata/repomd.xml.asc` (armored OpenPGP detached signature, binary document,
SHA-256) and `repodata/repomd.xml.key` (armored public key), then
`<project>.repo` in the repository root (only with `--repo-download-url`; has
`gpgcheck=1` and `gpgkey=<baseurl>repodata/repomd.xml.key` for signed
repositories, `gpgcheck=0` otherwise), and `repodata/repomd.xml` last.
