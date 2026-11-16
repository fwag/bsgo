package obsimport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/openSUSE/bsgo/pkg/constraints"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"gopkg.in/yaml.v3"
)

// OverridesFile is the path, inside the imported git ref, of the bsgo
// package overrides.
const OverridesFile = "_bsgo/packages.yaml"

// PackageOverride is what the overrides file can set on one generated package
// manifest. Only build.constraints.sandbox is supported.
type PackageOverride struct {
	Build struct {
		Constraints manifest.BuildConstraints `yaml:"constraints"`
	} `yaml:"build"`
}

// Overrides maps a package name to its overrides.
type Overrides struct {
	Packages map[string]PackageOverride `yaml:"packages"`
}

// ParseOverrides parses _bsgo/packages.yaml, for example:
//
//	packages:
//	  util-linux:
//	    build:
//	      constraints:
//	        sandbox: kvm
//
// Unknown keys are rejected so typos do not pass silently.
func ParseOverrides(data []byte) (*Overrides, error) {
	var o Overrides
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&o); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", OverridesFile, err)
	}
	for name, p := range o.Packages {
		if err := constraints.ValidateSandbox(p.Build.Constraints.Sandbox); err != nil {
			return nil, fmt.Errorf("%s: package %s: build.constraints.sandbox: %w", OverridesFile, name, err)
		}
	}
	return &o, nil
}

// Apply sets the overrides on the package manifests. It returns the number of
// packages that have an override and the names of overrides whose package was
// not imported, sorted.
func (o *Overrides) Apply(pkgs []*manifest.PackageManifest) (applied int, unused []string) {
	used := make(map[string]bool, len(o.Packages))
	for _, p := range pkgs {
		ov, ok := o.Packages[p.Package.Name]
		if !ok {
			continue
		}
		used[p.Package.Name] = true
		applied++
		if s := ov.Build.Constraints.Sandbox; s != "" {
			p.Build.Constraints.Sandbox = s
		}
	}
	unused = []string{}
	for name := range o.Packages {
		if !used[name] {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	return applied, unused
}
