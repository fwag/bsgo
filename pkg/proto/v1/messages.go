package v1

import (
	"time"

	"github.com/google/uuid"
)

type BuildTrigger struct {
	TriggerID     string `json:"trigger_id"`
	Project       string `json:"project"`
	Package       string `json:"package"`
	SourceGitURL  string `json:"source_git_url"`
	CommitSHA     string `json:"commit_sha"`
	Ref           string `json:"ref"`
	TriggeredBy   string `json:"triggered_by"`
	TimestampUnix int64  `json:"timestamp_unix"`
	ForceRebuild  bool   `json:"force_rebuild"`
}

type BuildJob struct {
	JobID         string               `json:"job_id"`
	Project       string               `json:"project"`
	Package       string               `json:"package"`
	Target        string               `json:"target"`
	Architecture  string               `json:"architecture"`
	Priority      int32                `json:"priority"`
	Source        SourceSpec           `json:"source"`
	ChrootRPMs    []ChrootRPM          `json:"chroot_rpms"`
	Resources     ResourceRequirements `json:"resources"`
	Destination   ArtifactDestination  `json:"destination"`
	CreatedAtUnix int64                `json:"created_at_unix"`
}

type SourceSpec struct {
	GitURL     string `json:"git_url"`
	CommitSHA  string `json:"commit_sha"`
	RecipePath string `json:"recipe_path"`
}

type ChrootRPM struct {
	Filename  string `json:"filename"`
	S3Key     string `json:"s3_key"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type ResourceRequirements struct {
	VCPUs          int32  `json:"vcpus"`
	MemoryMB       int64  `json:"memory_mb"`
	DiskGB         int64  `json:"disk_gb"`
	TimeoutSeconds int32  `json:"timeout_seconds"`
	SandboxType    string `json:"sandbox_type"`
}

type ArtifactDestination struct {
	S3Bucket string `json:"s3_bucket"`
	S3Prefix string `json:"s3_prefix"`
}

type JobControl struct {
	JobID         string `json:"job_id"`
	Action        string `json:"action"` // ABORT, CANCEL
	Reason        string `json:"reason"`
	TimestampUnix int64  `json:"timestamp_unix"`
}

type WorkerHeartbeat struct {
	WorkerID        string         `json:"worker_id"`
	Architecture    string         `json:"architecture"`
	Status          string         `json:"status"` // IDLE, BUSY, DRAINING
	ActiveJobID     string         `json:"active_job_id,omitempty"`
	Hardware        WorkerHardware `json:"hardware"`
	CachedRPMHashes []string       `json:"cached_rpm_hashes"`
	TimestampUnix   int64          `json:"timestamp_unix"`
}

type WorkerHardware struct {
	TotalVCPUs      int32    `json:"total_vcpus"`
	TotalMemoryMB   int64    `json:"total_memory_mb"`
	AvailableDiskGB int64    `json:"available_disk_gb"`
	SandboxType     string   `json:"sandbox_type"`
	CPUFeatures     []string `json:"cpu_features"`
}

func NewTrigger(project, pkg, gitURL, commit, ref, user string) *BuildTrigger {
	return &BuildTrigger{
		TriggerID:     uuid.NewString(),
		Project:       project,
		Package:       pkg,
		SourceGitURL:  gitURL,
		CommitSHA:     commit,
		Ref:           ref,
		TriggeredBy:   user,
		TimestampUnix: time.Now().Unix(),
	}
}
