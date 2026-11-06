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
└── publish.
    └── <type>.<project>.<target>.<arch> [JetStream: PUBLISH] Repodata / OCI publication requests
```

---

## 2. Stream Configurations & Retention Policies

| Stream Name | Subjects | Retention Policy | Storage | AckWait | MaxAge | Description |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **`TRIGGERS`** | `bsgo.triggers.>` | `Limits` | File | — | 7 Days | Immutable log of all inbound build trigger requests. |
| **`JOBS_PENDING`** | `bsgo.jobs.pending` | `WorkQueue` | File | 60s | 14 Days | Schedulers publish resolved jobs. Dispatcher consumes and assigns to workers. |
| **`JOB_STATUS`** | `bsgo.status` | `Limits` | File | — | 30 Days | Durable audit trail of job state transitions (`RUNNING`, `FAILED`, `SUCCEEDED`). |
| **`PUBLISH`** | `bsgo.publish.>` | `WorkQueue` | File | 300s | 3 Days | Serialized publication requests consumed by `bsgo-publisher`. |

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
}

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
  string sandbox_type = 5;    // "kvm", "zvm", "chroot"
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
  WorkerStatus status = 3;
  string active_job_id = 4;   // Present if status == WORKER_STATUS_BUSY
  
  WorkerHardware hardware = 5;
  repeated string cached_rpm_hashes = 6; // Hashes present in local worker SSD cache
  int64 timestamp_unix = 7;
}

message WorkerHardware {
  int32 total_vcpus = 1;
  int64 total_memory_mb = 2;
  int64 available_disk_gb = 3;
  string sandbox_type = 4;    // "kvm", "zvm"
  repeated string cpu_features = 5; // e.g. ["avx2", "avx512"]
}
```

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

### 3.7. Publication Request: `bsgo.publish.<type>.<project>.<target>.<arch>`
Emitted by the Dispatcher or Scheduler when a build finishes with new artifacts.

```protobuf
syntax = "proto3";
package bsgo.v1;

enum RepoType {
  REPO_TYPE_UNSPECIFIED = 0;
  REPO_TYPE_RPM = 1;
  REPO_TYPE_OCI = 2;
}

message PublishRequest {
  string request_id = 1;      // UUID
  string project = 2;
  string target = 3;
  string architecture = 4;
  RepoType repo_type = 5;
  string trigger_job_id = 6;
  repeated string new_s3_keys = 7;
  repeated string deleted_s3_keys = 8;
  int64 timestamp_unix = 9;
}
```
