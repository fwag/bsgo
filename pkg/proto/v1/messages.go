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
	Priority      string `json:"priority,omitempty"`     // "high", "med", "low" (default "high")
	Reason        string `json:"reason,omitempty"`       // "SOURCE_CHANGE", "ADMIN_TRIGGER", "CASCADE", "CONFIG_CHANGE"
	Target        string `json:"target,omitempty"`       // optional target override
	Architecture  string `json:"architecture,omitempty"` // optional arch override
}

// NormalizedPriority returns "high", "med", or "low".
func (t *BuildTrigger) NormalizedPriority() string {
	switch t.Priority {
	case "low":
		return "low"
	case "med", "medium":
		return "med"
	default:
		return "high"
	}
}

type BuildJob struct {
	JobID         string                 `json:"job_id"`
	Project       string                 `json:"project"`
	Package       string                 `json:"package"`
	Target        string                 `json:"target"`
	Architecture  string                 `json:"architecture"`
	Priority      int32                  `json:"priority"`
	Reason        string                 `json:"reason,omitempty"`
	Source        SourceSpec             `json:"source"`
	Repositories  []RepositoryDependency `json:"repositories,omitempty"`
	ChrootRPMs    []ChrootRPM            `json:"chroot_rpms"`
	Resources     ResourceRequirements   `json:"resources"`
	Destination   ArtifactDestination    `json:"destination"`
	CreatedAtUnix int64                  `json:"created_at_unix"`
}

type RepositoryDependency struct {
	Project  string `json:"project,omitempty"`
	Target   string `json:"target,omitempty"`
	Arch     string `json:"arch,omitempty"`
	S3Prefix string `json:"s3_prefix,omitempty"`
	URL      string `json:"url,omitempty"`
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

type PublishRequest struct {
	RequestID    string   `json:"request_id"`
	JobID        string   `json:"job_id,omitempty"`
	Project      string   `json:"project"`
	Target       string   `json:"target"`
	Architecture string   `json:"architecture"`
	Package      string   `json:"package,omitempty"`
	NewRPMPaths  []string `json:"new_rpm_paths,omitempty"`
	ArtifactKeys []string `json:"artifact_keys,omitempty"`
}

type PublicationEvent struct {
	EventID       string   `json:"event_id"`
	Project       string   `json:"project"`
	Target        string   `json:"target"`
	Architecture  string   `json:"architecture"`
	Package       string   `json:"package"`
	NewRPMs       []string `json:"new_rpms"`
	RepomdKey     string   `json:"repomd_key"`
	TimestampUnix int64    `json:"timestamp_unix"`
}

func NewPublicationEvent(project, target, arch, pkgName string, rpms []string, repomdKey string) *PublicationEvent {
	return &PublicationEvent{
		EventID:       uuid.NewString(),
		Project:       project,
		Target:        target,
		Architecture:  arch,
		Package:       pkgName,
		NewRPMs:       rpms,
		RepomdKey:     repomdKey,
		TimestampUnix: time.Now().Unix(),
	}
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
		Priority:      "high",
		Reason:        "SOURCE_CHANGE",
	}
}

func NewAdminTrigger(project, pkg, gitURL, commit, ref, user, target, arch, priority, reason string, force bool) *BuildTrigger {
	if priority == "" {
		priority = "high"
	}
	if reason == "" {
		reason = "ADMIN_TRIGGER"
	}
	return &BuildTrigger{
		TriggerID:     uuid.NewString(),
		Project:       project,
		Package:       pkg,
		SourceGitURL:  gitURL,
		CommitSHA:     commit,
		Ref:           ref,
		TriggeredBy:   user,
		TimestampUnix: time.Now().Unix(),
		ForceRebuild:  force,
		Priority:      priority,
		Reason:        reason,
		Target:        target,
		Architecture:  arch,
	}
}
