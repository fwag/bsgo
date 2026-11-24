package v1

import (
	"time"

	"github.com/google/uuid"
)

type BuildTrigger struct {
	TriggerID     string `json:"trigger_id"`
	Project       string `json:"project"`
	Package       string `json:"package"`
	Flavor        string `json:"flavor,omitempty"` // multibuild flavor of Package; empty for the plain package
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
	JobID        string `json:"job_id"`
	Project      string `json:"project"`
	Package      string `json:"package"`
	Target       string `json:"target"`
	Architecture string `json:"architecture"`
	Priority     int32  `json:"priority"`
	Reason       string `json:"reason,omitempty"`
	BuildType    string `json:"build_type,omitempty"`
	BuildEngine  string `json:"build_engine,omitempty"`
	// Legacy build inputs (BuildConfig .. Order, Repositories, ChrootRPMs).
	// LEGACY: superseded by BuildInfo. Kept so the pre-WP4 scheduler keeps
	// working; the worker only honours them when BuildInfo is nil.
	BuildConfig  string                 `json:"build_config,omitempty"`
	Macros       string                 `json:"macros,omitempty"`
	BuildFlags   []string               `json:"build_flags,omitempty"`
	Prefer       []string               `json:"prefer,omitempty"`
	Preinstall   []string               `json:"preinstall,omitempty"`
	VMinstall    []string               `json:"vminstall,omitempty"`
	Support      []string               `json:"support,omitempty"`
	Required     []string               `json:"required,omitempty"`
	Order        []string               `json:"order,omitempty"`
	Repositories []RepositoryDependency `json:"repositories,omitempty"`
	ChrootRPMs   []ChrootRPM            `json:"chroot_rpms"`

	Source SourceSpec `json:"source"`
	// BuildInfo is the complete, pre-resolved build contract (the bs_sched
	// "buildinfo"). When set the worker runs hermetically: it never resolves
	// dependencies itself and never passes --repo to obs-build.
	BuildInfo *BuildInfo           `json:"buildinfo,omitempty"`
	Resources ResourceRequirements `json:"resources"`
	// Constraints are the resolved build job constraints (OBS _constraints,
	// recipe and project configuration, plus the manifest override). Nil
	// means unconstrained.
	Constraints   *JobConstraints     `json:"constraints,omitempty"`
	Destination   ArtifactDestination `json:"destination"`
	CreatedAtUnix int64               `json:"created_at_unix"`
}

// BuildInfo carries everything the worker needs for a hermetic build. It is
// filled in by the scheduler; the worker must not invent any of it.
type BuildInfo struct {
	// Config is the worker variant of the layered project config, written to
	// .build.config and passed as --dist.
	Config string `json:"config"`
	// Bdeps is the ordered, fully expanded build dependency list.
	Bdeps []Bdep `json:"bdeps"`
	// ArchPath is the obs-build architecture path passed as --arch, for
	// example "x86_64:i686:i586:noarch". Provided by the scheduler.
	ArchPath string `json:"archpath"`
	// Release is the --release value (OBS bcnt based).
	Release string `json:"release,omitempty"`
	// DistURL is the --disturl value.
	DistURL string `json:"disturl,omitempty"`
	// Flavor is the multibuild flavor (--buildflavor).
	Flavor string `json:"flavor,omitempty"`
	// ObsPackage is the --obspackage value.
	ObsPackage string `json:"obspackage,omitempty"`
	// SourceCommit is the git commit id the sources must be checked out at.
	SourceCommit string `json:"source_commit"`
	// Meta is the build meta (bdep identity digest) echoed back in the result.
	Meta string `json:"meta,omitempty"`
	// LogIdleLimit aborts the build after this many seconds without log
	// output. Zero disables the limit. Enforced by the worker.
	LogIdleLimit int32 `json:"logidlelimit,omitempty"`
	// Jobs is the --jobs value. Zero lets the worker use Resources.VCPUs.
	Jobs int32 `json:"jobs,omitempty"`
}

// Bdep is one build dependency binary. SHA256 is the binary identity.
type Bdep struct {
	Name    string     `json:"name"`
	Epoch   string     `json:"epoch,omitempty"`
	Version string     `json:"version"`
	Release string     `json:"release"`
	Arch    string     `json:"arch"`
	SHA256  string     `json:"sha256"`
	Size    int64      `json:"size"`
	Source  BdepSource `json:"source"`

	Preinstall  bool `json:"preinstall,omitempty"`
	VMInstall   bool `json:"vminstall,omitempty"`
	RunScripts  bool `json:"runscripts,omitempty"`
	NoInstall   bool `json:"noinstall,omitempty"`
	InstallOnly bool `json:"installonly,omitempty"`
	// NotMeta excludes the bdep from the meta digest only. It has no rpmlist
	// representation (bs_worker does not write it either).
	NotMeta bool `json:"notmeta,omitempty"`
}

// BdepSource says where the worker fetches a bdep from. Exactly one of S3Key
// (own repositories, in the artifact bucket) or URL (DoD binaries: remote
// rpm-md base URL joined with the location href by the scheduler, absolute
// http or https) must be set.
type BdepSource struct {
	S3Key string `json:"s3_key,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Filename is the on-disk name of the bdep inside jobs/<id>/bdeps/.
func (b Bdep) Filename() string {
	return b.Name + "-" + b.Version + "-" + b.Release + "." + b.Arch + ".rpm"
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

// SandboxConstraint is one sandbox requirement: the worker sandbox must equal
// Value (or, for "secure", be a hardware-isolated sandbox), or must not when
// Exclude is set.
type SandboxConstraint struct {
	Value   string `json:"value"`
	Exclude bool   `json:"exclude,omitempty"`
}

// JobConstraints is the dispatch-relevant part of the merged build
// constraints. MemoryMB, DiskMB and Processors are already folded into
// Resources by the scheduler and are informational here.
type JobConstraints struct {
	Sandbox    []SandboxConstraint `json:"sandbox,omitempty"`
	MemoryMB   int64               `json:"memory_mb,omitempty"`
	DiskMB     int64               `json:"disk_mb,omitempty"`
	Processors int32               `json:"processors,omitempty"`
}

// secureSandboxes are the sandbox types the constraint "secure" accepts, like
// BSDispatcher::Constraints (chroot and nspawn share the host kernel).
var secureSandboxes = map[string]bool{"kvm": true, "xen": true, "zvm": true}

// SandboxConstrained reports whether the job restricts the worker sandbox.
func (c *JobConstraints) SandboxConstrained() bool {
	return c != nil && len(c.Sandbox) > 0
}

// SandboxAllows reports whether a worker running sandbox satisfies every
// sandbox constraint (BSDispatcher::Constraints::oracle).
func (c *JobConstraints) SandboxAllows(sandbox string) bool {
	if c == nil {
		return true
	}
	for _, s := range c.Sandbox {
		switch {
		case s.Exclude && s.Value == sandbox:
			return false
		case s.Exclude:
		case s.Value == "secure":
			if !secureSandboxes[sandbox] {
				return false
			}
		case s.Value != sandbox:
			return false
		}
	}
	return true
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
	WorkerID string `json:"worker_id"`
	// Architecture is the host architecture of the worker.
	Architecture string `json:"architecture"`
	// Archs lists the architectures the worker can build (the BSCando set of
	// the host, for example x86_64 builds x86_64, i586 and i686). Workers
	// that predate the field send none and build only Architecture.
	Archs           []string       `json:"archs,omitempty"`
	Status          string         `json:"status"` // IDLE, BUSY, DRAINING
	ActiveJobID     string         `json:"active_job_id,omitempty"`
	ActiveJobIDs    []string       `json:"active_job_ids,omitempty"`
	Hardware        WorkerHardware `json:"hardware"`
	CachedRPMHashes []string       `json:"cached_rpm_hashes"`
	// ConstrainedOnly makes the worker take only jobs with a sandbox
	// constraint (sandbox.accept_unconstrained: false). Workers that predate
	// the field accept every job.
	ConstrainedOnly bool  `json:"constrained_only,omitempty"`
	TimestampUnix   int64 `json:"timestamp_unix"`
}

// CanBuild reports whether the worker advertises arch.
func (h WorkerHeartbeat) CanBuild(arch string) bool {
	if len(h.Archs) == 0 {
		return h.Architecture == arch
	}
	for _, a := range h.Archs {
		if a == arch {
			return true
		}
	}
	return false
}

// ReasonWorkerCapacity is the reason of a REQUEUED job status a worker sends
// for a job that does not fit its free capacity: the dispatcher queues the job
// again; it is not a build failure.
const ReasonWorkerCapacity = "WORKER_CAPACITY"

type WorkerHardware struct {
	TotalVCPUs        int32    `json:"total_vcpus"`
	TotalMemoryMB     int64    `json:"total_memory_mb"`
	AvailableVCPUs    int32    `json:"available_vcpus"`
	AvailableMemoryMB int64    `json:"available_memory_mb"`
	AvailableDiskGB   int64    `json:"available_disk_gb"`
	SandboxType       string   `json:"sandbox_type"`
	CPUFeatures       []string `json:"cpu_features"`
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
