package checker

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

// expandUnits expands the build dependencies of every unit against the path
// pool (Build::get_deps). A failed expansion is kept on the unit with the
// expander's OBS error text.
func (ps *pass) expandUnits() error {
	for _, u := range ps.units {
		if u.fixed != nil {
			continue
		}
		u.deps = directDeps(u.info.Deps)
		res, err := ps.pool.GetDeps(ps.exp, u.info.Subpacks, u.info.Deps)
		var ee *solv.ExpandError
		switch {
		case err == nil:
			u.edeps = res
			if missing := missingChecksum(res); missing != "" {
				u.fixed = &verdict{"broken", "no sha256 checksum for " + missing + " in its repository metadata"}
			}
		case errors.As(err, &ee):
			u.expErr = ee
		default:
			return fmt.Errorf("expand %s: %w", u.name, err)
		}
	}
	return nil
}

func missingChecksum(res *solv.ExpansionResult) string {
	for _, p := range res.Packages {
		if p.SHA256 == "" {
			return p.Name
		}
	}
	return ""
}

// directDeps returns the package names a recipe requires literally.
func directDeps(deps []string) map[string]bool {
	out := map[string]bool{}
	for _, d := range deps {
		d = strings.TrimLeft(d, "-!")
		if f := strings.Fields(d); len(f) > 0 && !strings.HasPrefix(f[0], "(") {
			out[f[0]] = true
		}
	}
	return out
}

// depNames returns the binary package names unit u depends on: the expanded
// set, or the literal requirements when expansion failed (like OBS).
func (u *unit) depNames() []string {
	if u.edeps != nil {
		return u.edeps.Names()
	}
	out := make([]string, 0, len(u.deps))
	for d := range u.deps {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// buildProducers maps every binary name to the unit that produces it in this
// prpa: first by the binaries already in the own build repository, then by the
// subpackage names of the recipes of units that have not built yet. OBS
// derives dep2src the same way.
func (ps *pass) buildProducers() {
	ps.prod = map[string]*unit{}
	ps.own = map[string][]string{}
	if idx := ps.ownIndex(); idx != nil {
		for _, e := range idx.Entries {
			ps.own[e.Unit] = append(ps.own[e.Unit], e.Name)
			if u := ps.byName[e.Unit]; u != nil && ps.prod[e.Name] == nil {
				ps.prod[e.Name] = u
			}
		}
	}
	for _, u := range ps.units {
		if u.fixed != nil || u.info == nil {
			continue
		}
		for _, s := range u.info.Subpacks {
			if ps.prod[s] == nil {
				ps.prod[s] = u
			}
		}
	}
}

// ownIndex returns the index of the prpa's own build repository, or nil when
// it has no binaries yet.
func (ps *pass) ownIndex() *buildrepo.Index {
	for _, src := range ps.sources {
		if src.index != nil && src.elem.Project == ps.prpa.Project && src.elem.Repo == ps.prpa.Repo {
			return src.index
		}
	}
	return nil
}
