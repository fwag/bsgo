package obsimport

import (
	"fmt"
	"strings"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

// Result is the outcome of a conversion.
type Result struct {
	Manifest *manifest.ProjectManifest
	Report   Report
}

// Convert maps an OBS project (its _meta and prjconf text) to a manifest.
// It does not flatten conditionals: see the package documentation.
func Convert(meta *Meta, prjconf string) (*Result, error) {
	tree, err := buildTree(buildconfig.Items(prjconf))
	if err != nil {
		return nil, fmt.Errorf("prjconf: %w", err)
	}
	c := newClassifier(meta)
	c.walk(tree, nil)
	resolveOrder(c.events)

	prj := meta.project()
	a := &assembler{prj: prj, raw: map[string][]rawItem{}}
	for _, e := range c.events {
		a.place(e)
	}
	a.finish()
	rep := newReport(meta, c, a)
	return &Result{Manifest: prj, Report: rep}, nil
}

// rawItem is a verbatim fragment queued for a raw_config field.
type rawItem struct {
	text   string
	guards []*condNode
	event  *event
}

type assembler struct {
	prj        *manifest.ProjectManifest
	raw        map[string][]rawItem
	dests      map[string]dest
	structured int
}

// cfgFor returns the config object of a destination, creating it on demand.
func (a *assembler) cfgFor(d dest) *manifest.ProjectConfig {
	switch d.scope {
	case scopeProtected:
		return ensure(&a.prj.ProtectedConfig)
	case scopePrivate:
		return ensure(&a.prj.PrivateConfig)
	case scopeTarget:
		t := a.prj.Targets[d.repo]
		cfg := ensure(&t.Config)
		a.prj.Targets[d.repo] = t
		return cfg
	case scopeArch:
		return ensureArch(&a.prj.ArchConfig, d.arch)
	case scopeTargetArch:
		t := a.prj.Targets[d.repo]
		cfg := ensureArch(&t.ArchConfig, d.arch)
		a.prj.Targets[d.repo] = t
		return cfg
	default:
		return ensure(&a.prj.PublicConfig)
	}
}

func ensure(p **manifest.ProjectConfig) *manifest.ProjectConfig {
	if *p == nil {
		*p = &manifest.ProjectConfig{}
	}
	return *p
}

func ensureArch(m *map[string]*manifest.ProjectConfig, arch string) *manifest.ProjectConfig {
	if *m == nil {
		*m = map[string]*manifest.ProjectConfig{}
	}
	if (*m)[arch] == nil {
		(*m)[arch] = &manifest.ProjectConfig{}
	}
	return (*m)[arch]
}

func (a *assembler) place(e *event) {
	if e.demoted {
		a.queue(dest{scope: scopePublic}, rawItem{text: e.raw, guards: e.guards, event: e})
		return
	}
	for _, d := range e.dests {
		if e.kind == evRaw {
			a.queue(d, rawItem{text: e.raw, event: e})
			continue
		}
		e.apply(a.cfgFor(d))
		a.structured++
	}
}

func (a *assembler) queue(d dest, it rawItem) {
	if a.dests == nil {
		a.dests = map[string]dest{}
	}
	key := d.String()
	a.dests[key] = d
	a.raw[key] = append(a.raw[key], it)
}

// finish writes the queued raw fragments into their raw_config fields.
func (a *assembler) finish() {
	for key, items := range a.raw {
		a.cfgFor(a.dests[key]).RawConfig = joinRaw(items)
	}
}

// joinRaw concatenates fragments in source order, wrapping fragments that
// were moved out of a guard back into the guard. Consecutive fragments of
// the same guard chain share one wrapper.
func joinRaw(items []rawItem) string {
	var b strings.Builder
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && sameGuards(items[i].guards, items[j].guards) {
			j++
		}
		for _, g := range items[i].guards {
			b.WriteString(g.head.Raw + "\n")
		}
		for _, it := range items[i:j] {
			b.WriteString(strings.TrimSuffix(it.text, "\n") + "\n")
		}
		for k := len(items[i].guards) - 1; k >= 0; k-- {
			b.WriteString(items[i].guards[k].tail.Raw + "\n")
		}
		i = j
	}
	return b.String()
}

func sameGuards(a, b []*condNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
