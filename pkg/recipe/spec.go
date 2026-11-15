package recipe

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

var (
	tagLineRe     = regexp.MustCompile(`^(\S+)\s*:\s*(\S.*)`)
	hashBangRe    = regexp.MustCompile(`^(#!\S+)\s*`)
	exclArchRe    = regexp.MustCompile(`(?i)^ExclusiveArch\s*:`)
	sourceTagRe   = regexp.MustCompile(`(?i)^(Source|Patch)(\d*)\s*(\s+[^:]*?)?:\s*(\S+)`)
	packageRe     = regexp.MustCompile(`^\s*%package\s+(-n\s+)?(\S+)`)
	sectionRe     = regexp.MustCompile(`^\s*%(?:prep|generate_buildrequires|conf|build|install|check|clean|preun|postun|pretrans|posttrans|preuntrans|postuntrans|pre|post|files|changelog|description|triggerpostun|triggerprein|triggerun|triggerin|trigger|verifyscript|sepolicy|filetriggerin|filetrigger|filetriggerun|filetriggerpostun|transfiletriggerin|transfiletrigger|transfiletriggerun|transfiletriggerpostun|end|patchlist|sourcelist)\b`)
	depItemRe     = regexp.MustCompile(`([^\s,]+)(\s+[<=>]+\s+[^\s,]+)?(\s+\[[^\]]+\])?[\s,]*`)
	richDepRe     = regexp.MustCompile(`[\s,]\(`)
	leadSepRe     = regexp.MustCompile(`^[\s,]+`)
	qualSplitRe   = regexp.MustCompile(`[\s,]`)
	versOrderRe   = regexp.MustCompile(`=([<>])`)
	mainTagKeys   = map[string]bool{"name": true, "epoch": true, "version": true, "release": true, "disttag": true, "url": true}
	depKeywords   = map[string]bool{"buildrequires": true, "buildprereq": true, "buildconflicts": true, "#!buildignore": true, "#!buildconflicts": true, "#!buildrequires": true, "#!alsonative": true, "#!onlynative": true}
	requireKwords = map[string]bool{"buildrequires": true, "buildprereq": true, "#!buildrequires": true}
)

// specState is the state Build::Rpm::parse keeps between lines.
type specState struct {
	opt      Options
	info     *Info
	preamble bool
	autonum  map[string]int
	hasSub   bool
}

// parseSpec ports Build::Rpm::parse for the information a scheduler needs.
func parseSpec(cfg *buildconfig.Config, file, content string, opt Options) *Info {
	s := &specState{
		opt: opt, info: &Info{Deps: []string{}}, preamble: true,
		autonum: map[string]int{"patch": 0, "source": 0},
	}
	var packs []string
	s.info.Warnings = buildconfig.WalkSpec(cfg, content,
		buildconfig.SpecOptions{BuildFlavor: opt.Flavor, ObsPackage: opt.Package},
		func(line string, scope *buildconfig.SpecScope) { s.line(line, scope, &packs) })
	info := s.info
	if info.Name != "" {
		packs = append([]string{info.Name}, packs...)
	}
	info.Subpacks = append([]string{}, packs...)
	if info.Name == "" {
		info.Error = "can not parse name from " + file
	}
	return info
}

// line handles one expanded, non-skipped line.
func (s *specState) line(line string, scope *buildconfig.SpecScope, packs *[]string) {
	if !strings.ContainsAny(line, "%:!") {
		return
	}
	s.hasSub = len(*packs) > 0
	keyword, arg, hasArg := s.keyword(line)
	if keyword != "" && hasArg && s.tagWithArg(keyword, arg, scope) {
		return
	}
	if keyword != "" && !hasArg {
		s.flagTag(keyword)
		return
	}
	if s.preamble && s.sourceTag(line, scope) {
		return
	}
	s.section(line, packs)
}

// keyword extracts the preamble tag of a line.
func (s *specState) keyword(line string) (keyword, arg string, hasArg bool) {
	if !s.preamble {
		return "", "", false
	}
	if m := tagLineRe.FindStringSubmatch(line); m != nil {
		return strings.ToLower(m[1]), strings.TrimRight(m[2], " \t\r\n\f\v"), true
	}
	if m := hashBangRe.FindStringSubmatch(line); m != nil {
		return strings.ToLower(m[1]), "", false
	}
	if !s.hasSub && exclArchRe.MatchString(line) && s.info.ExclArch == nil {
		s.info.ExclArch = []string{}
	}
	return "", "", false
}

func (s *specState) flagTag(keyword string) {
	switch keyword {
	case "#!forcemultiversion":
		s.info.MultiVersion = true
	case "#!nativebuild":
		s.info.NativeBuild = true
	}
}

// tagWithArg handles "Tag: value" lines; it reports whether the line was
// consumed (Perl's "next if $keyword").
func (s *specState) tagWithArg(keyword, arg string, scope *buildconfig.SpecScope) bool {
	if !s.hasSub {
		s.mainTag(keyword, arg, scope)
	}
	switch {
	case keyword == "version":
		first := firstWord(arg)
		if s.hasSub && s.info.Version != "" && s.info.Version != first {
			s.info.MultiVersion = true
		}
	case keyword == "#!buildtarget":
		s.buildTarget(arg, scope)
	case keyword == "#!buildconstraint":
		s.info.Constraints = append(s.info.Constraints, arg)
	case keyword == "#!bcntsynctag", keyword == "#!remoteasset", keyword == "#!remoteasseturl", keyword == "#!createarchive":
	case depKeywords[keyword]:
		s.deps(keyword, arg, scope)
	case keyword == "url":
		scope.SetMacro("url", arg)
	case keyword == "icon", keyword == "requires(pre)", keyword == "requires(post)", keyword == "prereq":
	default:
		return false
	}
	return true
}

// mainTag records the identity tags of the main package preamble.
func (s *specState) mainTag(keyword, arg string, scope *buildconfig.SpecScope) {
	switch {
	case mainTagKeys[keyword]:
		w := firstWord(arg)
		switch keyword {
		case "name":
			s.info.Name = w
		case "epoch":
			s.info.Epoch = w
		case "version":
			s.info.Version = w
		case "release":
			s.info.Release = w
		}
		scope.SetMacro(keyword, w)
		scope.SetMacro(strings.ToUpper(keyword), w)
	case keyword == "exclusivearch":
		if s.info.ExclArch == nil {
			s.info.ExclArch = []string{}
		}
		s.info.ExclArch = append(s.info.ExclArch, strings.Fields(arg)...)
	case keyword == "excludearch":
		if s.info.BadArch == nil {
			s.info.BadArch = []string{}
		}
		s.info.BadArch = append(s.info.BadArch, strings.Fields(arg)...)
	}
}

// buildTarget ports the #!BuildTarget handling: "[arch:]target".
func (s *specState) buildTarget(arg string, scope *buildconfig.SpecScope) {
	arg = firstWord(arg)
	if i := strings.IndexByte(arg, ':'); i >= 0 {
		pre := arg[:i]
		arg = arg[i+1:]
		cpu := scope.Macro("_target_cpu")
		if cpu == "" {
			cpu = "unknown"
		}
		if pre != "" && pre != cpu {
			arg = ""
		}
	}
	s.info.BuildTarget = ""
	if arg != "" && arg != "0" {
		s.info.BuildTarget = arg
	}
}

// deps handles the dependency-bearing tags.
func (s *specState) deps(keyword, arg string, scope *buildconfig.SpecScope) {
	var ndeps []string
	for _, d := range splitDepList(arg) {
		if d.qual != "" && qualifierMismatch(scope, d.qual) {
			continue
		}
		ndeps = append(ndeps, d.pack+versOrderRe.ReplaceAllString(d.vers, "$1="))
	}
	switch keyword {
	case "#!alsonative":
		s.info.AlsoNative = append(s.info.AlsoNative, ndeps...)
	case "#!onlynative":
		s.info.OnlyNative = append(s.info.OnlyNative, ndeps...)
	default:
		if requireKwords[keyword] {
			s.info.Deps = append(s.info.Deps, ndeps...)
			return
		}
		prefix := "-"
		if s.opt.ConflictDeps && keyword != "#!buildignore" {
			prefix = "!"
		}
		for _, d := range ndeps {
			s.info.Deps = append(s.info.Deps, prefix+d)
		}
	}
}

type depItem struct{ pack, vers, qual string }

// splitDepList splits a dependency tag value like Build::Rpm::parse does,
// including its rich dependency handling.
func splitDepList(arg string) []depItem {
	if richDepRe.MatchString(" " + arg) {
		return splitRichDeps(arg)
	}
	var out []depItem
	for _, m := range depItemRe.FindAllStringSubmatch(arg, -1) {
		out = append(out, depItem{m[1], m[2], m[3]})
	}
	return out
}

// splitRichDeps ports splitdeps/shiftrich.
func splitRichDeps(d string) []depItem {
	var out []depItem
	d = leadSepRe.ReplaceAllString(d, "")
	for d != "" {
		if strings.HasPrefix(d, "(") {
			f := strings.Fields(d)
			dep := f[0]
			f = f[1:]
			for len(f) > 0 && strings.Count(dep, "(") > strings.Count(dep, ")") {
				dep += " " + f[0]
				f = f[1:]
			}
			out = append(out, depItem{pack: dep})
			d = strings.Join(f, " ")
			continue
		}
		loc := depItemRe.FindStringSubmatchIndex(d)
		if loc == nil {
			break
		}
		out = append(out, depItem{pack: d[loc[2]:loc[3]], vers: sub(d, loc[4], loc[5]), qual: sub(d, loc[6], loc[7])})
		d = d[:loc[0]] + d[loc[1]:]
	}
	return out
}

func sub(s string, a, b int) string {
	if a < 0 {
		return ""
	}
	return s[a:b]
}

// qualifierMismatch ports dep_qualifier_mismatch including its quirks: the
// negation flag is never reset between qualifiers.
func qualifierMismatch(scope *buildconfig.SpecScope, qual string) bool {
	arch := scope.Macro("_target_cpu")
	proj := scope.Macro("_target_project")
	qual = strings.TrimLeft(qual, " \t\n\r\f")
	qual = strings.TrimPrefix(qual, "[")
	qual = strings.TrimSuffix(qual, "]")
	isneg, bad, badSet := false, false, false
	for _, q := range perlSplit(qualSplitRe, qual) {
		if strings.HasPrefix(q, "!") {
			q = q[1:]
			isneg = true
		}
		if !badSet && !isneg {
			bad, badSet = true, true
		}
		if q == arch || q == proj {
			if isneg {
				return false
			}
			bad, badSet = false, true
		}
	}
	return !bad
}

// perlSplit splits like Perl's split(/re/, s): trailing empty fields are
// dropped.
func perlSplit(re *regexp.Regexp, s string) []string {
	parts := re.Split(s, -1)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// sourceTag records the SourceN/PatchN tag macros (SOURCEURLn, PATCHURLn).
func (s *specState) sourceTag(line string, scope *buildconfig.SpecScope) bool {
	m := sourceTagRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	typ := strings.ToLower(m[1])
	num := s.autonum[typ]
	if m[2] != "" {
		num, _ = strconv.Atoi(m[2])
	}
	if num >= s.autonum[typ] {
		s.autonum[typ] = num + 1
	}
	scope.SetMacro(strings.ToUpper(typ)+"URL"+strconv.Itoa(num), m[4])
	return true
}

// section tracks %package subpackages and the end of the preamble.
func (s *specState) section(line string, packs *[]string) {
	if m := packageRe.FindStringSubmatch(line); m != nil {
		switch {
		case m[1] != "":
			*packs = append(*packs, m[2])
		case s.info.Name != "":
			*packs = append(*packs, s.info.Name+"-"+m[2])
		default:
			*packs = append(*packs, "unknown-"+m[2])
		}
		s.preamble = true
		return
	}
	if sectionRe.MatchString(line) {
		s.preamble = false
	}
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}
