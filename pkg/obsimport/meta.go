// Package obsimport converts OBS project data (the project _meta XML and the
// project configuration, "prjconf") into a bsgo project manifest.
//
// The conversion never flattens conditional branches (ADR 0010). Constructs
// that map onto a structured scope are stored there; everything else is kept
// verbatim in raw_config and reported. Correctness is defined by the
// round-trip gate: for every architecture, repository and Consumer Project,
// the layered and parsed manifest must equal the parsed original prjconf.
package obsimport

import (
	"encoding/xml"
	"errors"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

// Meta is the subset of an OBS project _meta that becomes a manifest.
type Meta struct {
	Name         string
	Title        string
	Description  string
	SCMSync      string
	Maintainers  []string
	Repositories []Repository
	build        flagSet
	publish      flagSet
	debuginfo    flagSet
}

// Repository is one OBS repository: a build target.
type Repository struct {
	Name   string
	Arches []string
	Paths  []PathRef
}

// PathRef is one <path> element of a repository.
type PathRef struct {
	Project    string
	Repository string
}

type xmlFlag struct {
	XMLName    xml.Name
	Repository string `xml:"repository,attr"`
}

type flagSet []xmlFlag

type xmlPerson struct {
	UserID string `xml:"userid,attr"`
	Role   string `xml:"role,attr"`
}

type xmlGroup struct {
	GroupID string `xml:"groupid,attr"`
	Role    string `xml:"role,attr"`
}

type xmlRepo struct {
	Name  string `xml:"name,attr"`
	Paths []struct {
		Project    string `xml:"project,attr"`
		Repository string `xml:"repository,attr"`
	} `xml:"path"`
	Arches []string `xml:"arch"`
}

type xmlProject struct {
	Name        string      `xml:"name,attr"`
	Title       string      `xml:"title"`
	Description string      `xml:"description"`
	SCMSync     string      `xml:"scmsync"`
	Persons     []xmlPerson `xml:"person"`
	Groups      []xmlGroup  `xml:"group"`
	Build       struct {
		Flags flagSet `xml:",any"`
	} `xml:"build"`
	Publish struct {
		Flags flagSet `xml:",any"`
	} `xml:"publish"`
	Debuginfo struct {
		Flags flagSet `xml:",any"`
	} `xml:"debuginfo"`
	Repos []xmlRepo `xml:"repository"`
}

// ParseMeta parses an OBS project _meta document.
func ParseMeta(data []byte) (*Meta, error) {
	var x xmlProject
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse _meta: %w", err)
	}
	if x.Name == "" {
		return nil, errors.New("parse _meta: project has no name attribute")
	}
	m := &Meta{
		Name: x.Name, Title: x.Title, Description: x.Description, SCMSync: x.SCMSync,
		build: x.Build.Flags, publish: x.Publish.Flags, debuginfo: x.Debuginfo.Flags,
	}
	for _, p := range x.Persons {
		if p.UserID != "" && (p.Role == "" || p.Role == "maintainer") {
			m.Maintainers = appendUnique(m.Maintainers, p.UserID)
		}
	}
	for _, g := range x.Groups {
		if g.GroupID != "" && (g.Role == "" || g.Role == "maintainer") {
			m.Maintainers = appendUnique(m.Maintainers, "group:"+g.GroupID)
		}
	}
	for _, r := range x.Repos {
		repo := Repository{Name: r.Name, Arches: r.Arches}
		for _, p := range r.Paths {
			repo.Paths = append(repo.Paths, PathRef{Project: p.Project, Repository: p.Repository})
		}
		m.Repositories = append(m.Repositories, repo)
	}
	return m, nil
}

func appendUnique(list []string, s string) []string {
	for _, e := range list {
		if e == s {
			return list
		}
	}
	return append(list, s)
}

// value evaluates a flag for a repository: unrestricted entries first, then
// entries naming the repository. Without any matching entry the default
// applies.
func (f flagSet) value(repo string, def bool) bool {
	v := def
	for _, scoped := range []bool{false, true} {
		for _, e := range f {
			if (e.Repository != "") != scoped || (scoped && e.Repository != repo) {
				continue
			}
			v = e.XMLName.Local == "enable"
		}
	}
	return v
}

// project builds the manifest skeleton (identity and targets) of the meta.
func (m *Meta) project() *manifest.ProjectManifest {
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name: m.Name, Title: m.Title, Description: m.Description,
			SCMSync: m.SCMSync, Maintainers: m.Maintainers,
		},
		Targets: map[string]manifest.BuildTarget{},
	}
	for _, r := range m.Repositories {
		t := manifest.BuildTarget{
			Architectures: r.Arches,
			Flags: manifest.TargetFlags{
				Build:     m.build.value(r.Name, true),
				Publish:   m.publish.value(r.Name, true),
				Debuginfo: m.debuginfo.value(r.Name, true),
			},
		}
		for _, p := range r.Paths {
			t.Dependencies = append(t.Dependencies, manifest.DependencySource{Project: p.Project, Target: p.Repository})
		}
		prj.Targets[r.Name] = t
	}
	return prj
}

func (m *Meta) hasRepo(name string) bool {
	for _, r := range m.Repositories {
		if r.Name == name {
			return true
		}
	}
	return false
}
