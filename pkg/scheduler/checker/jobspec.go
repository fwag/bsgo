package checker

import (
	"github.com/openSUSE/bsgo/pkg/constraints"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

// JobSpec is the complete build contract the checker hands to a JobSink. A
// sink maps it onto the wire format (v1.BuildJob with BuildInfo); the field
// names follow that message.
type JobSpec struct {
	// JobID is deterministic per (prpa, unit, attempt), so re-emitting after a
	// crashed pass does not create a second job.
	JobID string
	// Project, Repo and Arch identify the prpa.
	Project, Repo, Arch string
	// Package is the unit name "pkg" or "pkg:flavor"; BasePackage and Flavor
	// are its parts (ObsPackage and Flavor of BuildInfo).
	Package, BasePackage, Flavor string
	// SourceURL, SourceCommit and RecipeFile identify the sources: the full
	// git commit id of the manifest pin and the selected recipe.
	SourceURL, SourceCommit, RecipeFile string
	// Branch is the manifest branch, informational only.
	Branch string
	// BuildType is the recipe type, for example "spec".
	BuildType string
	// Config is the layered worker configuration text (Macros kept).
	Config string
	// Bdeps is the ordered, fully expanded build dependency list: preinstalls,
	// vminstalls, expanded dependencies, then the remaining packages.
	Bdeps []Bdep
	// Archpath is the --arch value.
	Archpath string
	// Release is the --release value after <CI_CNT>/<B_CNT> substitution.
	Release string
	// CICnt and BCnt are the counters Release was computed from.
	CICnt, BCnt int
	// DistURL is the --disturl value; it embeds the source commit.
	DistURL string
	// Meta is the hash of the build meta, the tree of dependencies the build
	// is made from (see metatree.go).
	Meta string
	// Explain says why the job was created: "new build", "source change",
	// "meta change" or "forced rebuild".
	Explain string
	// Priority orders jobs of one dispatch queue; higher runs earlier. It is
	// 50 plus the number of units that depend on this one.
	Priority int32
	// LogIdleLimit is the BuildFlags logidlelimit in seconds, 0 when unset.
	LogIdleLimit int32
	// Resources are the manifest's resource limits, unparsed.
	Resources manifest.ResourceLimits
	// Constraints are the merged build job constraints (OBS _constraints,
	// recipe, project configuration and the manifest override).
	Constraints constraints.Constraints
}

// Bdep is one build dependency binary.
type Bdep struct {
	Name    string
	Epoch   string
	Version string
	Release string
	Arch    string
	// SHA256 is the binary identity.
	SHA256 string
	Size   int64
	// Source says where the binary comes from: a build repository of this or
	// another prpa, or a DoD repository.
	Source BdepSource

	Preinstall bool
	VMInstall  bool
	RunScripts bool
	// NotMeta marks packages that do not take part in the meta digest.
	NotMeta bool
}

// BdepSource locates a dependency binary: exactly one of the fields is set.
type BdepSource struct {
	// S3Key is the object key in the artifact bucket (a build repository).
	S3Key string
	// URL is the absolute http(s) URL of a binary in a remote rpm-md (DoD)
	// repository: its base URL joined with the package location.
	URL string
}
