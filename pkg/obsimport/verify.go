package obsimport

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

// Mismatch is one failed cell of the round-trip matrix.
type Mismatch struct {
	Consumer string   `json:"consumer"`
	Repo     string   `json:"repo"`
	Arch     string   `json:"arch"`
	Output   string   `json:"output"`
	Fields   []string `json:"fields"`
}

// Verify runs the ADR 0010 round-trip gate for a converted project: for every
// repository of prj, every architecture in arches and the Consumer Projects
// (the project itself, a subproject and a foreign project), the layered and
// parsed manifest must equal the original prjconf assembled the way OBS
// getconfig does. Both the scheduler and the worker text are compared. Parser
// warnings are not compared: they carry line numbers of the generated text.
func Verify(prj *manifest.ProjectManifest, prjconf string, arches []string) ([]Mismatch, error) {
	name := prj.Project.Name
	repos := make([]string, 0, len(prj.Targets))
	for r := range prj.Targets {
		repos = append(repos, r)
	}
	slices.Sort(repos)
	load := func(p string) (*manifest.ProjectManifest, error) {
		if p == name {
			return prj, nil
		}
		return nil, nil
	}
	var out []Mismatch
	for _, repo := range repos {
		for _, consumer := range []string{name, name + ":Sub", "home:verify"} {
			ms, err := verifyCell(prj, prjconf, repo, consumer, arches, load)
			if err != nil {
				return nil, err
			}
			out = append(out, ms...)
		}
	}
	return out, nil
}

func verifyCell(prj *manifest.ProjectManifest, prjconf, repo, consumer string, arches []string, load buildconfig.Loader) ([]Mismatch, error) {
	name := prj.Project.Name
	gotS, gotW, err := buildconfig.Layer(consumer, repo, []buildconfig.PathElem{{Project: name, Repo: repo}}, load)
	if err != nil {
		return nil, err
	}
	wantS, wantW := buildconfig.LayerTexts(consumer, []buildconfig.Source{{Project: name, Repo: repo, Text: prjconf}})
	var out []Mismatch
	for _, arch := range arches {
		for _, c := range []struct{ output, got, want string }{
			{"scheduler", gotS, wantS}, {"worker", gotW, wantW},
		} {
			fields, err := compareParsed(c.got, c.want, arch)
			if err != nil {
				return nil, fmt.Errorf("%s/%s consumer %s arch %s: %w", name, repo, consumer, arch, err)
			}
			if len(fields) > 0 {
				out = append(out, Mismatch{Consumer: consumer, Repo: repo, Arch: arch, Output: c.output, Fields: fields})
			}
		}
	}
	return out, nil
}

// compareParsed parses both texts and returns the names of the Config fields
// that differ.
func compareParsed(got, want, arch string) ([]string, error) {
	g, gerr := buildconfig.Parse(got, arch)
	w, werr := buildconfig.Parse(want, arch)
	if (gerr == nil) != (werr == nil) {
		return nil, fmt.Errorf("parse error differs: converted %v, original %v", gerr, werr)
	}
	return DiffConfig(Canonical(g), Canonical(w)), nil
}

// setLists are the Config lists that act as sets: a converted manifest may
// order their entries differently than the original prjconf, because scopes
// are rendered in a fixed order. They are compared sorted. Every other field,
// including all ordered lists (Preinstall, Vminstall, Macros, RawMacros), is
// compared exactly. The hash values of multi-valued flags (excludebuild,
// dockerarg) are ignored. Warnings are dropped: they carry line numbers of the
// generated text.
func setLists(c *buildconfig.Config) []*[]string {
	return []*[]string{&c.Prefer, &c.Ignore, &c.Conflict, &c.Support, &c.Required,
		&c.ExpandFlags, &c.BuildFlags, &c.PublishFlags}
}

// Canonical returns a copy of c with the set-like lists sorted and Warnings
// dropped; see setLists.
func Canonical(c *buildconfig.Config) *buildconfig.Config {
	cp := *c
	cp.Warnings = nil
	for _, l := range setLists(&cp) {
		*l = sortedCopy(*l)
	}
	cp.BuildFlagsH = dropMultiValues(c.BuildFlagsH)
	cp.ExpandFlagsH = dropMultiValues(c.ExpandFlagsH)
	cp.PublishFlagsH = dropMultiValues(c.PublishFlagsH)
	cp.ConflictH = make(map[string][]string, len(c.ConflictH))
	for k, v := range c.ConflictH {
		cp.ConflictH[k] = sortedCopy(v)
	}
	return &cp
}

// dropMultiValues blanks the hash value of multi-valued flags: OBS reads them
// from the flag list, so the "last one wins" value is an artifact of order.
func dropMultiValues(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if multiFlags[k] {
			v = ""
		}
		out[k] = v
	}
	return out
}

func sortedCopy(l []string) []string {
	out := slices.Clone(l)
	slices.Sort(out)
	return out
}

// DiffConfig returns the names of the Config fields that differ.
func DiffConfig(a, b *buildconfig.Config) []string {
	var out []string
	va, vb := reflect.ValueOf(*a), reflect.ValueOf(*b)
	for i := 0; i < va.NumField(); i++ {
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			out = append(out, va.Type().Field(i).Name)
		}
	}
	return out
}
