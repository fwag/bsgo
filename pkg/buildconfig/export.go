package buildconfig

import (
	"fmt"
	"regexp"
	"sort"
)

// ExportOwn is the ExportFilter target that stands for the architecture of
// the build itself.
const ExportOwn = "."

// defaultExportFilters are the filters OBS applies when the configuration has
// no ExportFilter at all (BSSched::BuildResult %default_exportfilters). Debug
// packages are dropped everywhere; the baselibs of the 32 bit and 64 bit
// twins are exported to the sibling architecture.
var defaultExportFilters = map[string]map[string][]string{
	"i586":     withDebugDrop(map[string][]string{`\.x86_64\.rpm$`: {"x86_64"}, `\.ia64\.rpm$`: {"ia64"}}),
	"x86_64":   withDebugDrop(nil),
	"ppc":      withDebugDrop(map[string][]string{`\.ppc64\.rpm$`: {"ppc64"}}),
	"ppc64":    withDebugDrop(map[string][]string{`\.ppc\.rpm$`: {"ppc"}}),
	"sparc":    withDebugDrop(map[string][]string{`\.sparc64\.rpm$`: {}}),
	"sparcv8":  withDebugDrop(map[string][]string{`\.sparc64\.rpm$`: {}}),
	"sparcv9":  withDebugDrop(map[string][]string{`\.sparc64\.rpm$`: {"sparc64"}}),
	"sparcv9v": withDebugDrop(map[string][]string{`\.sparc64v\.rpm$`: {"sparc64v"}}),
	"sparc64":  withDebugDrop(map[string][]string{`\.sparcv9\.rpm$`: {"sparcv9"}}),
	"sparc64v": withDebugDrop(map[string][]string{`\.sparcv9v\.rpm$`: {"sparcv9v"}}),
}

func withDebugDrop(m map[string][]string) map[string][]string {
	out := map[string][]string{`-debuginfo-.*\.rpm$`: {}, `-debugsource-.*\.rpm$`: {}}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ExportRouter decides to which architectures a binary is exported. It
// follows BSSched::BuildResult::set_suf_and_filter_exports: the rules are
// tried in reverse order of their regular expression text and the first rule
// that matches the file name decides.
type ExportRouter struct {
	arch  string
	rules []exportRule
}

type exportRule struct {
	re      *regexp.Regexp
	targets []string
}

// NewExportRouter compiles an ExportFilter for builds of arch. An empty
// filter is replaced by the OBS default of the architecture.
func NewExportRouter(filter map[string][]string, arch string) (*ExportRouter, error) {
	if len(filter) == 0 {
		filter = defaultExportFilters[arch]
	}
	patterns := make([]string, 0, len(filter))
	for rx := range filter {
		patterns = append(patterns, rx)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(patterns)))
	r := &ExportRouter{arch: arch}
	for _, rx := range patterns {
		re, err := regexp.Compile(rx)
		if err != nil {
			return nil, fmt.Errorf("ExportFilter %q: %w", rx, err)
		}
		r.rules = append(r.rules, exportRule{re: re, targets: filter[rx]})
	}
	return r, nil
}

// Route returns whether the binary with file name filename stays in the
// repository of the building architecture and the other architectures it is
// exported to (sorted, without duplicates). A binary matching no rule stays;
// a matching rule without targets drops the binary entirely.
func (r *ExportRouter) Route(filename string) (own bool, others []string) {
	for _, rule := range r.rules {
		if !rule.re.MatchString(filename) {
			continue
		}
		seen := map[string]bool{}
		for _, t := range rule.targets {
			switch {
			case t == ExportOwn || t == r.arch:
				own = true
			case !seen[t]:
				seen[t] = true
				others = append(others, t)
			}
		}
		sort.Strings(others)
		return own, others
	}
	return true, nil
}
