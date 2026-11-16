// Package recipe parses build recipes (RPM spec files and _multibuild
// definitions) into the information a build scheduler needs: package
// identity, build dependencies and architecture restrictions. The spec parser
// is a behavior-compatible port of the dependency-relevant part of obs-build's
// Build::Rpm::parse and reuses the rpm macro engine of package buildconfig, so
// the macros of the layered project configuration apply exactly like in OBS.
package recipe

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Info is the result of parsing one recipe. Field semantics follow the hash
// returned by Build::parse in obs-build.
type Info struct {
	// Name, Version, Release and Epoch come from the main preamble; empty
	// when the tag is absent.
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Release string `json:"release,omitempty"`
	Epoch   string `json:"epoch,omitempty"`
	// Deps lists BuildRequires/BuildPrereq/#!BuildRequires entries as "name"
	// or "name op evr" (rich dependencies verbatim), in recipe order.
	// BuildConflicts and #!BuildIgnore entries appear in the same list as
	// "-name" (or "!name" for conflicts with Options.ConflictDeps), exactly
	// like Build::Rpm::parse emits them.
	Deps []string `json:"deps"`
	// Prereqs lists the names of Requires(pre), Requires(post) and PreReq
	// tags of all (sub)packages, unversioned and deduplicated in first-seen
	// order, with file paths kept (obs-build "prereqs" with
	// unfilteredprereqs). Use BuildPrereqs for the build root additions.
	Prereqs []string `json:"prereqs,omitempty"`
	// Subpacks lists the main package followed by the %package subpackages.
	Subpacks []string `json:"subpacks"`
	// ExclArch and BadArch are ExclusiveArch and ExcludeArch; nil when the
	// tags are absent (an ExclusiveArch tag without arguments yields an empty
	// non-nil list, as in Perl).
	ExclArch []string `json:"exclarch,omitempty"`
	BadArch  []string `json:"badarch,omitempty"`
	// BuildTarget is the #!BuildTarget value for the configured arch.
	BuildTarget string `json:"buildtarget,omitempty"`
	// Constraints holds the #!BuildConstraint lines in order.
	Constraints []string `json:"constraints,omitempty"`
	// AlsoNative and OnlyNative hold #!AlsoNative and #!OnlyNative entries.
	AlsoNative []string `json:"alsonative,omitempty"`
	OnlyNative []string `json:"onlynative,omitempty"`
	// MultiVersion is set by #!ForceMultiVersion or differing subpackage
	// versions.
	MultiVersion bool `json:"multiversion,omitempty"`
	// NativeBuild is set by #!NativeBuild.
	NativeBuild bool `json:"nativebuild,omitempty"`
	// Warnings are parser diagnostics, such as "cannot expand %(...)". Perl
	// prints these to stderr; they do not fail the parse.
	Warnings []string `json:"warnings,omitempty"`
	// Error is a recipe-level error as bs_srcserver reports it, for example
	// "can not parse name from foo.spec". Info is otherwise unusable.
	Error string `json:"error,omitempty"`
}

// BuildPrereqs returns the prereqs that OBS adds to the build root: those
// that are neither a build dependency, nor a subpackage name nor start with
// "%" (bs_srcserver and BSRepServer::BuildInfo).
func (i *Info) BuildPrereqs() []string {
	var out []string
	for _, p := range i.Prereqs {
		if strings.HasPrefix(p, "%") || slices.Contains(i.Deps, p) || slices.Contains(i.Subpacks, p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Options tune one Parse call.
type Options struct {
	// Package is the base package name, substituted for @OBS_PACKAGE@ (for a
	// flavor unit "pkg:flavor" this is "pkg").
	Package string
	// Flavor is the multibuild flavor substituted for @BUILD_FLAVOR@; empty
	// for the plain package.
	Flavor string
	// ConflictDeps emits BuildConflicts as "!name" instead of "-name"
	// ($Build::Rpm::conflictdeps).
	ConflictDeps bool
}

// ErrUnsupported is matched by errors.Is for recipe types without a parser.
var ErrUnsupported = errors.New("unsupported recipe type")

// UnsupportedError reports a recipe whose build type is not parsed yet.
type UnsupportedError struct {
	Path string
	Type string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("recipe %s: %s recipes are not supported yet", e.Path, e.Type)
}

// Is makes errors.Is(err, ErrUnsupported) succeed.
func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }
