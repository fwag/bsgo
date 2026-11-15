package obsimport

import (
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

type applyFn = func(*manifest.ProjectConfig)

// dirSpec converts the argument text of a directive. It returns the function
// that stores the value, a key identifying the value for single-valued
// fields (a second value for the same key stays verbatim), and the reason
// when the line cannot be stored structurally.
type dirSpec func(args string) (apply applyFn, key, reason string)

// directiveSpecs maps lowercase prjconf keywords to structured fields.
// FromScratch is handled by the classifier.
var directiveSpecs = map[string]dirSpec{
	"preinstall":   listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.Preinstall }),
	"vminstall":    listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.VMinstall }),
	"support":      listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.Support }),
	"required":     listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.Required }),
	"order":        listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.Order }),
	"prefer":       listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.Prefer }),
	"ignore":       listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.Ignore }),
	"expandflags":  listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.ExpandFlags }),
	"buildflags":   listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.BuildFlags }),
	"publishflags": listSpec(func(c *manifest.ProjectConfig) *[]string { return &c.PublishFlags }),
	"conflict":     conflictSpec,
	"constraint":   constraintSpec,
	"substitute":   mapSpec(func(c *manifest.ProjectConfig) *map[string][]string { return &c.Substitute }),
	"fileprovides": mapSpec(func(c *manifest.ProjectConfig) *map[string][]string { return &c.FileProvides }),
	"optflags":     optflagsSpec,
	"exportfilter": exportFilterSpec,
	"release":      releaseSpec,
	"buildengine":  buildEngineSpec,
	"type":         typeSpec,
	"repotype":     repotypeSpec,
}

// fieldsSafe reports whether args can be split on white space without
// changing how the macro engine sees them: brackets balance per token and no
// dynamic macro (%{expand:..}, %{lua:..}, %(..), %[..]) is used.
func fieldsSafe(args string) bool {
	if dynamicLike(args) {
		return false
	}
	for _, f := range strings.Fields(args) {
		if strings.Count(f, "{") != strings.Count(f, "}") {
			return false
		}
	}
	return true
}

func dynamicLike(s string) bool {
	return dynamicRe.MatchString(s)
}

// entries splits list arguments per token, or keeps them as one verbatim
// entry when splitting is unsafe.
func entries(args string) []string {
	if fieldsSafe(args) {
		return strings.Fields(args)
	}
	return []string{args}
}

func listSpec(field func(*manifest.ProjectConfig) *[]string) dirSpec {
	return func(args string) (applyFn, string, string) {
		if args == "" {
			return nil, "", "empty list directive"
		}
		items := entries(args)
		return func(c *manifest.ProjectConfig) {
			f := field(c)
			*f = append(*f, items...)
		}, "", ""
	}
}

func conflictSpec(args string) (applyFn, string, string) {
	if args == "" {
		return nil, "", "empty list directive"
	}
	row := entries(args)
	return func(c *manifest.ProjectConfig) { c.Conflict = append(c.Conflict, row) }, "", ""
}

func constraintSpec(args string) (applyFn, string, string) {
	if args == "" || !fieldsSafe(args) {
		return nil, "", "constraint is empty or uses dynamic macros"
	}
	entry := strings.Join(strings.Fields(args), " ")
	return func(c *manifest.ProjectConfig) { c.Constraints = append(c.Constraints, entry) }, "", ""
}

// plainFields returns the tokens of args when the directive can be stored as
// a keyed value: no macro reference at all, and no "!" removal form.
func plainFields(args string, min int) ([]string, bool) {
	f := strings.Fields(args)
	if len(f) < min || strings.Contains(args, "%") || strings.HasPrefix(f[0], "!") {
		return nil, false
	}
	return f, true
}

const keyedReason = "keyed directive uses macros, a removal form or too few arguments"

func mapSpec(field func(*manifest.ProjectConfig) *map[string][]string) dirSpec {
	return func(args string) (applyFn, string, string) {
		f, ok := plainFields(args, 1)
		if !ok {
			return nil, "", keyedReason
		}
		return func(c *manifest.ProjectConfig) {
			m := field(c)
			if *m == nil {
				*m = map[string][]string{}
			}
			(*m)[f[0]] = f[1:]
		}, f[0], ""
	}
}

func optflagsSpec(args string) (applyFn, string, string) {
	f, ok := plainFields(args, 2)
	if !ok {
		return nil, "", keyedReason
	}
	val := strings.Join(f[1:], " ")
	return func(c *manifest.ProjectConfig) {
		if c.Optflags == nil {
			c.Optflags = map[string]string{}
		}
		c.Optflags[f[0]] = val
	}, f[0], ""
}

func exportFilterSpec(args string) (applyFn, string, string) {
	f, ok := plainFields(args, 1)
	if !ok {
		return nil, "", keyedReason
	}
	entry := strings.Join(f, " ")
	return func(c *manifest.ProjectConfig) { c.ExportFilter = append(c.ExportFilter, entry) }, f[0], ""
}

func releaseSpec(args string) (applyFn, string, string) {
	if args == "" || !fieldsSafe(args) {
		return nil, "", "release is empty or uses dynamic macros"
	}
	val := strings.Join(strings.Fields(args), " ")
	return func(c *manifest.ProjectConfig) { c.Release = val }, "release", ""
}

func buildEngineSpec(args string) (applyFn, string, string) {
	f := strings.Fields(args)
	if len(f) != 1 || !fieldsSafe(args) {
		return nil, "", "buildengine needs exactly one plain value"
	}
	return func(c *manifest.ProjectConfig) { c.BuildEngine = f[0] }, "buildengine", ""
}

func typeSpec(args string) (applyFn, string, string) {
	f := strings.Fields(args)
	if len(f) == 0 || !fieldsSafe(args) {
		return nil, "", "type is empty or uses dynamic macros"
	}
	return func(c *manifest.ProjectConfig) { c.BuildTypes = []string{f[0]} }, "type", ""
}

func repotypeSpec(args string) (applyFn, string, string) {
	f := strings.Fields(args)
	if len(f) == 0 || !fieldsSafe(args) {
		return nil, "", "repotype is empty or uses dynamic macros"
	}
	return func(c *manifest.ProjectConfig) { c.RepoTypes = f }, "repotype", ""
}

// Directives that fill a set: appends of different entries commute. Only a
// "!" removal form is order sensitive. buildconfig.Config does not give
// these lists a meaning by position (the solver and the flag hashes use
// them as sets), so converted and original configs may differ in their order
// (see Verify).
var setDirectives = map[string]bool{
	"prefer": true, "ignore": true, "support": true, "required": true,
	"conflict": true, "order": true,
}

// flagDirectives hold name or name:value flags; a flag name behaves like a
// map key, so different names commute.
var flagDirectives = map[string]bool{"buildflags": true, "expandflags": true, "publishflags": true}

// keyedDirectives hold one value per key; different keys commute.
var keyedDirectives = map[string]bool{
	"substitute": true, "fileprovides": true, "optflags": true, "exportfilter": true,
}

// multiFlags are flags that may be given many times: OBS reads all values
// from the flag list (BSSched/Checker.pm, ProjPacks.pm). They commute, and
// the flag hash entry written by Build::read_config is not meaningful.
var multiFlags = map[string]bool{"excludebuild": true, "dockerarg": true}

// effects returns the state a directive writes and reads, for the
// reordering analysis. Keyed and set-like directives only write the narrow
// key they touch and read the directive name, so that lines of the same
// directive that cannot be keyed (which write the directive name) still
// conflict with them.
func effects(kw, args string) (writes, reads []string) {
	name := "d:" + kw
	switch {
	case keyedDirectives[kw]:
		f, ok := plainFields(args, 1)
		if !ok {
			return []string{name}, nil
		}
		return []string{name + ":" + f[0]}, []string{name}
	case setDirectives[kw] && !hasRemoval(args):
		return nil, []string{name}
	case flagDirectives[kw] && !hasRemoval(args) && fieldsSafe(args):
		for _, f := range strings.Fields(args) {
			flag, _, _ := strings.Cut(f, ":")
			if !multiFlags[flag] {
				writes = append(writes, name+":"+flag)
			}
		}
		return writes, []string{name}
	}
	return []string{name}, nil
}

func hasRemoval(args string) bool {
	for _, f := range strings.Fields(args) {
		if strings.HasPrefix(f, "!") {
			return true
		}
	}
	return false
}
