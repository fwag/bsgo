package obsimport

import (
	"regexp"
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

type scopeKind int

// Scope kinds, in Render order (also the order used to detect reordering).
const (
	scopePublic scopeKind = iota
	scopeProtected
	scopePrivate
	scopeTarget
	scopeArch
	scopeTargetArch
)

// dest is a place in the manifest an event is stored in.
type dest struct {
	scope scopeKind
	repo  string
	arch  string
}

func (d dest) String() string {
	switch d.scope {
	case scopeProtected:
		return "protected_config"
	case scopePrivate:
		return "private_config"
	case scopeTarget:
		return "targets." + d.repo + ".config"
	case scopeArch:
		return "arch_config." + d.arch
	case scopeTargetArch:
		return "targets." + d.repo + ".arch_config." + d.arch
	default:
		return "public_config"
	}
}

// compatible reports whether two destinations can be active together.
func (d dest) compatible(o dest) bool {
	if d.repo != "" && o.repo != "" && d.repo != o.repo {
		return false
	}
	return d.arch == "" || o.arch == "" || d.arch == o.arch
}

type evKind int

const (
	evDefine evKind = iota
	evDirective
	evRaw
	evMacros
)

// event is one convertible piece of the prjconf in source order.
type event struct {
	kind   evKind
	line   int
	dests  []dest
	guards []*condNode
	raw    string
	// body is the verbatim Macros: body of an evMacros event.
	body  string
	apply func(*manifest.ProjectConfig)
	// reason says why a raw event is raw.
	reason string
	// demoted events were moved to the public raw_config to keep the order.
	demoted   bool
	demotedBy string
	writes    []string
	reads     []string
}

// inPublicRaw reports whether the event is rendered as public raw_config.
func (e *event) inPublicRaw() bool {
	if e.demoted {
		return true
	}
	if e.kind != evRaw {
		return false
	}
	for _, d := range e.dests {
		if d.scope != scopePublic {
			return false
		}
	}
	return true
}

// rank orders destination and kind as Render emits them: scope, then
// defines, directives, raw_config, Macros.
func (e *event) rank(d dest) int {
	if e.inPublicRaw() {
		return 2
	}
	sub := map[evKind]int{evDefine: 0, evDirective: 1, evRaw: 2, evMacros: 3}[e.kind]
	return int(d.scope)*10 + sub
}

func (e *event) effectiveDests() []dest {
	if e.demoted {
		return []dest{{scope: scopePublic}}
	}
	return e.dests
}

var (
	nameRe    = `[A-Za-z_][A-Za-z0-9_]*`
	refRe     = regexp.MustCompile(`%\{?[?!]*-?(` + nameRe + `)`)
	writeRe   = regexp.MustCompile(`(?m)^\s*%(?:define|global|undefine)\s+(` + nameRe + `)`)
	dynamicRe = regexp.MustCompile(`%\{(?:expand|lua)\b|%\(|%\[`)
)

// analyze fills reads and writes from the verbatim text of the event.
func (e *event) analyze(text string) {
	e.reads = append(e.reads, refKeys(text)...)
	for _, m := range writeRe.FindAllStringSubmatch(text, -1) {
		e.writes = append(e.writes, "m:"+m[1])
	}
	for _, line := range strings.Split(text, "\n") {
		if m := keywordLn.FindStringSubmatch(line); m != nil {
			w, r := effects(strings.ToLower(m[1]), m[2])
			e.writes = append(e.writes, w...)
			e.reads = append(e.reads, r...)
		}
	}
}

func refKeys(text string) []string {
	var keys []string
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		keys = append(keys, "m:"+m[1])
	}
	if dynamicRe.MatchString(text) {
		keys = append(keys, "*")
	}
	return keys
}
