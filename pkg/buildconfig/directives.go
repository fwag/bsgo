package buildconfig

import (
	"strings"
)

// directives applies the expanded configuration lines to a Config, like the
// directive loop in Build::read_config.
type directives struct {
	cfg           *Config
	noFromScratch bool
}

// handler processes one directive. args are the whitespace separated fields
// after the keyword and line is the full expanded line. It returns false
// when the directive must not count as "content" for the FromScratch rule.
type handler func(d *directives, line string, args []string) bool

// listDirectives maps list-valued directives to their Config field.
var listDirectives = map[string]func(*Config) *[]string{
	"preinstall:":    func(c *Config) *[]string { return &c.Preinstall },
	"vminstall:":     func(c *Config) *[]string { return &c.Vminstall },
	"required:":      func(c *Config) *[]string { return &c.Required },
	"support:":       func(c *Config) *[]string { return &c.Support },
	"keep:":          func(c *Config) *[]string { return &c.Keep },
	"prefer:":        func(c *Config) *[]string { return &c.Prefer },
	"ignore:":        func(c *Config) *[]string { return &c.Ignore },
	"conflict:":      func(c *Config) *[]string { return &c.Conflict },
	"runscripts:":    func(c *Config) *[]string { return &c.Runscripts },
	"expandflags:":   func(c *Config) *[]string { return &c.ExpandFlags },
	"buildflags:":    func(c *Config) *[]string { return &c.BuildFlags },
	"publishflags:":  func(c *Config) *[]string { return &c.PublishFlags },
	"repourl:":       func(c *Config) *[]string { return &c.RepoURL },
	"registryurl:":   func(c *Config) *[]string { return &c.RegistryURL },
	"assetsurl:":     func(c *Config) *[]string { return &c.AssetsURL },
	"onlynative:":    func(c *Config) *[]string { return &c.OnlyNative },
	"alsonative:":    func(c *Config) *[]string { return &c.AlsoNative },
	"ignorerebuild:": func(c *Config) *[]string { return &c.IgnoreRebuild },
}

// scalarDirectives maps directives taking their first argument verbatim.
var scalarDirectives = map[string]func(*Config) *string{
	"type:":         func(c *Config) *string { return &c.Type },
	"buildengine:":  func(c *Config) *string { return &c.BuildEngine },
	"binarytype:":   func(c *Config) *string { return &c.BinaryType },
	"cicntstart:":   func(c *Config) *string { return &c.CICntStart },
	"releaseprg:":   func(c *Config) *string { return &c.ReleasePrg },
	"singleexport:": func(c *Config) *string { return &c.SingleExport },
}

// joinedDirectives map directives whose arguments are joined by a space.
var joinedDirectives = map[string]func(*Config) *string{
	"releasesuffix:": func(c *Config) *string { return &c.ReleaseSuffix },
	"hostarch:":      func(c *Config) *string { return &c.HostArch },
}

var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		"macros:":       (*directives).macros,
		"distmacro:":    (*directives).distMacro,
		"substitute:":   mapDirective(func(c *Config) map[string][]string { return c.Substitute }),
		"fileprovides:": mapDirective(func(c *Config) map[string][]string { return c.FileProvides }),
		"exportfilter:": (*directives).exportFilter,
		"publishfilter:": func(d *directives, _ string, a []string) bool {
			d.cfg.PublishFilter = append([]string{}, a...)
			return true
		},
		"optflags:":     (*directives).optflags,
		"order:":        (*directives).order,
		"repotype:":     listReplace(func(c *Config) *[]string { return &c.Repotype }),
		"patterntype:":  listReplace(func(c *Config) *[]string { return &c.Patterntype }),
		"release:":      (*directives).release,
		"changetarget:": (*directives).target,
		"target:":       (*directives).target,
		"constraint:":   (*directives).constraint,
		"fromscratch:":  (*directives).fromScratch,
	}
	for k, f := range listDirectives {
		handlers[k] = listDirective(f)
	}
	for k, f := range scalarDirectives {
		handlers[k] = scalarDirective(f)
	}
	for k, f := range joinedDirectives {
		handlers[k] = joinedDirective(f)
	}
}

// apply processes one expanded line.
func (d *directives) apply(entry string) {
	f := splitWS(entry)
	if len(f) == 0 {
		return
	}
	kw := strings.ToLower(f[0])
	if h, ok := handlers[kw]; ok {
		if h(d, entry, f[1:]) {
			d.noFromScratch = true
		}
		return
	}
	if kw[0] == '#' || kw[0] == '%' {
		return
	}
	d.cfg.Warnings = append(d.cfg.Warnings, "unknown keyword in config: "+kw)
	d.noFromScratch = true
}

func (d *directives) macros(line string, _ []string) bool {
	body := line
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		body = line[i+1:]
	}
	if strings.HasPrefix(body, "!\n") {
		d.cfg.RawMacros = body[2:]
	} else {
		d.cfg.RawMacros += body
	}
	return true
}

func (d *directives) distMacro(line string, _ []string) bool {
	if l := splitWS2(line); len(l) == 2 {
		d.cfg.Macros = append(d.cfg.Macros, "%define "+l[1])
	}
	return true
}

// applyList implements the append / "!name" removal / "!*" reset semantics
// shared by list directives.
func applyList(dst *[]string, items []string) {
	for _, it := range items {
		switch {
		case it == "!*":
			*dst = []string{}
		case strings.HasPrefix(it, "!"):
			kept := (*dst)[:0:0]
			for _, e := range *dst {
				if e != it[1:] {
					kept = append(kept, e)
				}
			}
			*dst = kept
		default:
			*dst = append(*dst, it)
		}
	}
}

func listDirective(f func(*Config) *[]string) handler {
	return func(d *directives, _ string, a []string) bool {
		applyList(f(d.cfg), a)
		return true
	}
}

func listReplace(f func(*Config) *[]string) handler {
	return func(d *directives, _ string, a []string) bool {
		*f(d.cfg) = append([]string{}, a...)
		return true
	}
}

func scalarDirective(f func(*Config) *string) handler {
	return func(d *directives, _ string, a []string) bool {
		*f(d.cfg) = argAt(a, 0)
		return true
	}
}

func joinedDirective(f func(*Config) *string) handler {
	return func(d *directives, _ string, a []string) bool {
		*f(d.cfg) = strings.Join(a, " ")
		return true
	}
}

// mapDirective implements Substitute: and FileProvides: (key, values).
func mapDirective(f func(*Config) map[string][]string) handler {
	return func(d *directives, _ string, a []string) bool {
		if len(a) == 0 {
			return false
		}
		m := f(d.cfg)
		switch key := a[0]; {
		case key == "!*":
			clear(m)
		case strings.HasPrefix(key, "!"):
			delete(m, key[1:])
		default:
			m[key] = append([]string{}, a[1:]...)
		}
		return true
	}
}

func (d *directives) exportFilter(_ string, a []string) bool {
	if len(a) == 0 {
		return false
	}
	d.cfg.ExportFilter[a[0]] = append([]string{}, a[1:]...)
	return true
}

func (d *directives) optflags(_ string, a []string) bool {
	if len(a) == 0 {
		return false
	}
	d.cfg.Optflags[a[0]] = strings.Join(a[1:], " ")
	return true
}

func (d *directives) order(_ string, a []string) bool {
	for _, x := range a {
		switch {
		case x == "!*":
			clear(d.cfg.Order)
		case strings.HasPrefix(x, "!"):
			delete(d.cfg.Order, x[1:])
		default:
			d.cfg.Order[x] = true
		}
	}
	return true
}

func (d *directives) release(_ string, a []string) bool {
	d.cfg.Release = argAt(a, 0)
	d.cfg.ReleaseList = append([]string{}, a...)
	return true
}

func (d *directives) target(_ string, a []string) bool {
	d.cfg.Target = strings.Join(a, " ")
	if perlTrue(d.cfg.Target) {
		cpu, _, _ := strings.Cut(d.cfg.Target, "-")
		d.cfg.Macros = append(d.cfg.Macros, "%define _target_cpu "+cpu)
	}
	return true
}

func (d *directives) constraint(_ string, a []string) bool {
	joined := strings.Join(a, " ")
	if joined == "!*" {
		d.cfg.Constraint = []string{}
	} else {
		d.cfg.Constraint = append(d.cfg.Constraint, joined)
	}
	return true
}

func (d *directives) fromScratch(string, []string) bool {
	if d.noFromScratch {
		d.cfg.ParseError = "FromScratch directive must come first"
	}
	return false
}
