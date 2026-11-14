package v1

import (
	"strings"
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
	BuildType     string                 `json:"build_type,omitempty"`
	BuildEngine   string                 `json:"build_engine,omitempty"`
	BuildConfig   string                 `json:"build_config,omitempty"`
	Macros        string                 `json:"macros,omitempty"`
	BuildFlags    []string               `json:"build_flags,omitempty"`
	Prefer        []string               `json:"prefer,omitempty"`
	Preinstall    []string               `json:"preinstall,omitempty"`
	VMinstall     []string               `json:"vminstall,omitempty"`
	Support       []string               `json:"support,omitempty"`
	Required      []string               `json:"required,omitempty"`
	Order         []string               `json:"order,omitempty"`
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
	Branch     string `json:"branch,omitempty"`
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
	ActiveJobIDs    []string       `json:"active_job_ids,omitempty"`
	Hardware        WorkerHardware `json:"hardware"`
	CachedRPMHashes []string       `json:"cached_rpm_hashes"`
	TimestampUnix   int64          `json:"timestamp_unix"`
}

type WorkerHardware struct {
	TotalVCPUs        int32    `json:"total_vcpus"`
	TotalMemoryMB     int64    `json:"total_memory_mb"`
	AvailableVCPUs    int32    `json:"available_vcpus,omitempty"`
	AvailableMemoryMB int64    `json:"available_memory_mb,omitempty"`
	AvailableDiskGB   int64    `json:"available_disk_gb"`
	SandboxType       string   `json:"sandbox_type"`
	CPUFeatures       []string `json:"cpu_features"`
}

type PublishRequest struct {
	RequestID     string     `json:"request_id"`
	JobID         string     `json:"job_id,omitempty"`
	Project       string     `json:"project"`
	Target        string     `json:"target"`
	Architecture  string     `json:"architecture"`
	Package       string     `json:"package,omitempty"`
	RepoTypes     []string   `json:"repo_types,omitempty"`
	NewRPMPaths   []string   `json:"new_rpm_paths,omitempty"`
	ArtifactKeys  []string   `json:"artifact_keys,omitempty"`
	DeletedRPMs   []string   `json:"deleted_rpms,omitempty"`
	SubRepos      []string   `json:"sub_repos,omitempty"`
	Advisories    []Advisory `json:"advisories,omitempty"`
	AppStreamData []byte     `json:"appstream_data,omitempty"`
	AppStreamPath string     `json:"appstream_path,omitempty"`
}

// PackageDeletionRequest requests removal of a package and its associated RPM binaries from published repodata.
type PackageDeletionRequest struct {
	RequestID    string   `json:"request_id"`
	Project      string   `json:"project"`
	Target       string   `json:"target"`
	Architecture string   `json:"architecture"`
	Package      string   `json:"package"`
	DeletedRPMs  []string `json:"deleted_rpms,omitempty"`
}

// Advisory defines an errata or security advisory for updateinfo.xml (e.g. for zypper patch / zypper cve).
type Advisory struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Severity    string            `json:"severity,omitempty"` // "low", "moderate", "important", "critical"
	Type        string            `json:"type"`               // "security", "bugfix", "enhancement"
	Description string            `json:"description,omitempty"`
	IssuedDate  string            `json:"issued_date,omitempty"`
	Release     string            `json:"release,omitempty"`
	References  []AdvisoryRef     `json:"references,omitempty"`
	Packages    []AdvisoryPackage `json:"packages,omitempty"`
}

// AdvisoryRef represents a reference URL (e.g. CVE or Bugzilla) for an advisory.
type AdvisoryRef struct {
	Type  string `json:"type"` // "cve", "bugzilla", "issue"
	ID    string `json:"id"`
	Href  string `json:"href"`
	Title string `json:"title,omitempty"`
}

// AdvisoryPackage represents a package version contained in an errata advisory.
type AdvisoryPackage struct {
	Name     string `json:"name"`
	Epoch    string `json:"epoch,omitempty"`
	Version  string `json:"version"`
	Release  string `json:"release"`
	Arch     string `json:"arch"`
	Filename string `json:"filename"`
}

// HasSubRepo reports whether the given sub-repository (e.g. "standard", "debug", "src") is explicitly requested.
func (r *PublishRequest) HasSubRepo(sr string) bool {
	sr = strings.ToLower(strings.TrimSpace(sr))
	for _, s := range r.SubRepos {
		if strings.ToLower(strings.TrimSpace(s)) == sr {
			return true
		}
	}
	return false
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
