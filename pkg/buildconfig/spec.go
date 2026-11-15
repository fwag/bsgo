package buildconfig

import (
	"regexp"
	"strconv"
	"strings"
)

// This file ports the part of Build::Rpm::parse that runs in "parsing_config"
// mode: line continuation handling, macro expansion and the %if family of
// conditionals. Its output is the list of expanded lines that the directive
// handlers consume.

var (
	neededForBuildRe = regexp.MustCompile(`^#\s*neededforbuild\s*(\S.*)$`)
	defineStartRe    = regexp.MustCompile(`(?s)^\s*%(?:define|global)\s`)
	condStartRe      = regexp.MustCompile(`^\s*%\{[?!]*-?[0-9a-zA-Z_]+:\s*$`)
	commentRe        = regexp.MustCompile(`^\s*#`)
	elseLikeRe       = regexp.MustCompile(`^\s*%(?:else|endif|elif|elifarch|elifos)\b`)
	elseRe           = regexp.MustCompile(`^\s*%else`)
	endifRe          = regexp.MustCompile(`^\s*%endif`)
	ifPrefixRe       = regexp.MustCompile(`^\s*%if`)
	elifPrefixRe     = regexp.MustCompile(`^(\s*%)el`)
	ifRe             = regexp.MustCompile(`\s*%(if|ifarch|ifnarch|ifos|ifnos)\b(.*)`)
	lineMarkRe       = regexp.MustCompile(`^#!!line (?:(\S*?):)?([0-9]+)\s*$`)
	macrosOpenRe     = regexp.MustCompile(`(?i)^\s*macros:\s*$`)
	macrosCloseRe    = regexp.MustCompile(`(?i)^\s*:macros\s*$`)
	wsRunRe          = regexp.MustCompile(`\s+`)
	newlineRe        = regexp.MustCompile(`\n`)
)

// inputLine is one logical input item: a physical line or a verbatim
// Macros: block that must not be interpreted.
type inputLine struct {
	text     string
	verbatim bool
}

// specLine is one recorded output entry. undef entries were skipped by a
// conditional.
type specLine struct {
	text  string
	undef bool
}

// parseState carries the diagnostics context shared with the macro engine.
type parseState struct {
	file       string
	lineno     int
	parseError string
}

// warn ports do_warn for config parsing: the first error wins.
func (s *parseState) warn(msg string) {
	linestr := s.file
	if s.lineno != 0 {
		if linestr != "" {
			linestr += ":"
		}
		linestr = "line " + linestr + strconv.Itoa(s.lineno)
	}
	if linestr != "" {
		msg = linestr + ": " + msg
	}
	if s.parseError == "" {
		s.parseError = msg
	}
}

// splitLines splits file content the way reading and chomping a file does.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// collectMacroBlobs folds every Macros: ... :Macros block into one verbatim
// item whose text is "macros:\n" followed by the block body.
func collectMacroBlobs(lines []string) []inputLine {
	var out []inputLine
	for len(lines) > 0 {
		l := lines[0]
		lines = lines[1:]
		if !macrosOpenRe.MatchString(l) {
			out = append(out, inputLine{text: l})
			continue
		}
		var b strings.Builder
		b.WriteString("macros:\n")
		for len(lines) > 0 {
			next := lines[0]
			lines = lines[1:]
			if macrosCloseRe.MatchString(next) {
				break
			}
			b.WriteString(next + "\n")
		}
		out = append(out, inputLine{text: b.String(), verbatim: true})
	}
	return out
}

type specParser struct {
	st        *parseState
	eng       *macroEngine
	skip      int
	multiDef  *string
	multiCond *string
	include   []string
	out       []specLine
	// Spec mode (Build::Rpm::parse outside parsing_config): the build flavor
	// and package name substituted for @BUILD_FLAVOR@ and @OBS_PACKAGE@, and
	// the callback receiving every line that survived macro expansion and
	// conditionals. A nil visit selects config mode.
	flavor     string
	obsPackage string
	visit      func(line string)
}

func (p *specParser) run(src []inputLine) []specLine {
	for {
		var line string
		record := true
		switch {
		case len(p.include) > 0:
			line = p.include[0]
			p.include = p.include[1:]
			record = false
		case len(src) > 0:
			it := src[0]
			src = src[1:]
			p.st.lineno++
			if it.verbatim {
				p.out = append(p.out, specLine{text: it.text, undef: p.skip != 0})
				continue
			}
			line = it.text
		default:
			return p.out
		}
		if record {
			p.out = append(p.out, specLine{text: line})
		}
		p.processLine(line, record)
	}
}

func (p *specParser) markUndef(record bool) {
	if record {
		p.out[len(p.out)-1].undef = true
	}
}

func (p *specParser) processLine(line string, record bool) {
	if neededForBuildRe.MatchString(line) {
		return
	}
	line, ok := p.joinContinuations(line)
	if !ok {
		return
	}
	if commentRe.MatchString(line) && !strings.HasPrefix(line, "#!") {
		return
	}
	line = p.expandLine(line)
	line, ok = p.conditionals(line, record)
	if !ok {
		return
	}
	line = strings.ReplaceAll(line, "@BUILD_FLAVOR@", p.flavor)
	if p.obsPackage != "" {
		line = strings.ReplaceAll(line, "@OBS_PACKAGE@", p.obsPackage)
	}
	if p.handleIf(line) {
		return
	}
	p.finish(line, record)
}

// joinContinuations merges backslash-continued %define lines and multi-line
// %{?x: ... } conditions. ok is false while more input is needed.
func (p *specParser) joinContinuations(line string) (string, bool) {
	if p.multiDef != nil || defineStartRe.MatchString(line) {
		if p.multiDef != nil {
			line = *p.multiDef + line
		}
		p.multiDef = nil
		if strings.HasSuffix(line, "\\") {
			s := line[:len(line)-1] + "\n"
			p.multiDef = &s
			return "", false
		}
	}
	if p.multiCond != nil || condStartRe.MatchString(line) {
		if p.multiCond != nil {
			line = *p.multiCond + "\n" + line
		}
		p.multiCond = nil
		if needMoreLines(line) {
			p.multiCond = &line
			return "", false
		}
	}
	return line, true
}

// expandLine expands macros unless the line is inside a skipped branch.
func (p *specParser) expandLine(line string) string {
	if p.skip != 0 || !strings.Contains(line, "%") {
		return line
	}
	line = p.eng.expand(line, 0)
	if strings.Contains(line, "\n") {
		line = p.splitExpansion(line)
	}
	return line
}

// splitExpansion ports splitexpansionresult: only the first line of a
// multi-line expansion is kept, the rest is queued for re-parsing.
func (p *specParser) splitExpansion(line string) string {
	parts := splitRe(newlineRe, line)
	if len(parts) == 0 {
		return ""
	}
	rest := make([]string, 0, len(parts)-1)
	for _, l := range parts[1:] {
		rest = append(rest, strings.ReplaceAll(l, "%", "%%"))
	}
	p.include = append(rest, p.include...)
	return parts[0]
}

// conditionals handles skipping logic for %else, %endif, %elif and lines
// inside skipped branches. ok is false when the line was consumed.
func (p *specParser) conditionals(line string, record bool) (string, bool) {
	if !elseLikeRe.MatchString(line) {
		if p.skip == 0 {
			return line, true
		}
		if ifPrefixRe.MatchString(line) {
			p.skip += 2
		}
		p.markUndef(record)
		return "", false
	}
	switch {
	case elseRe.MatchString(line):
		if p.skip <= 2 {
			p.skip = 2 - p.skip
		}
		return "", false
	case endifRe.MatchString(line):
		if p.skip > 2 {
			p.skip -= 2
		} else {
			p.skip = 0
		}
		return "", false
	}
	if p.skip == 0 {
		p.skip = 1
	}
	if p.skip <= 2 {
		p.skip = 2 - p.skip
	}
	if p.skip != 0 {
		return "", false
	}
	line = elifPrefixRe.ReplaceAllString(line, "${1}")
	return p.expandLine(line), true
}

// handleIf evaluates %if, %ifarch, %ifnarch, %ifos and %ifnos. The match is
// deliberately unanchored, as in obs-build.
func (p *specParser) handleIf(line string) bool {
	m := ifRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	kind, arg := m[1], m[2]
	switch kind {
	case "if":
		v, _ := evalExpr(arg, nil, p.st.warn)
		if !exprBoolify(v) {
			p.skip = 2
		}
	case "ifarch", "ifnarch":
		p.skipUnless(kind == "ifarch", p.eng.macros["_target_cpu"], arg)
	default:
		p.skipUnless(kind == "ifos", p.eng.macros["_target_os"], arg)
	}
	return true
}

// skipUnless starts skipping when membership of value in the list arg does
// not match want.
func (p *specParser) skipUnless(want bool, value, arg string) {
	if value == "" {
		value = "unknown"
	}
	found := false
	for _, f := range splitRe(wsRunRe, arg) {
		if f == value {
			found = true
		}
	}
	if found != want {
		p.skip = 2
	}
}

func (p *specParser) finish(line string, record bool) {
	if p.visit != nil {
		p.visit(line)
		return
	}
	if m := lineMarkRe.FindStringSubmatch(line); m != nil {
		if m[1] != "" {
			p.st.file = m[1]
		}
		p.st.lineno, _ = strconv.Atoi(m[2])
	}
	if record {
		p.out[len(p.out)-1].text = line
	}
}

// needMoreLines ports needmorelines (rpm's copyNextLineFromOFI): it reports
// whether brackets or a continuation are still open.
func needMoreLines(line string) bool {
	var bc, pc, xc, nc int
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == '%' && i+1 < len(line) && line[i+1] == '{':
			bc++
			i += 2
		case c == '%' && i+1 < len(line) && line[i+1] == '(':
			pc++
			i += 2
		case c == '%' && i+1 < len(line) && line[i+1] == '[':
			xc++
			i += 2
		case c == '%':
			i++
			if i < len(line) && line[i] == '%' {
				i++
			}
		case c == '\n':
			nc = 0
			i++
		case c == '\\':
			if i+1 < len(line) && line[i+1] == '\n' {
				nc = 1
			}
			i = backslashEnd(line, i)
		default:
			bc, pc, xc = closeBracket(c, bc, pc, xc)
			i++
		}
	}
	return pc != 0 || bc != 0 || xc != 0 || nc != 0
}

// backslashEnd returns the index after the backslash escape at i.
func backslashEnd(line string, i int) int {
	if i+1 < len(line) {
		return i + 2
	}
	return i + 1
}

func closeBracket(c byte, bc, pc, xc int) (int, int, int) {
	switch c {
	case '{':
		if bc != 0 {
			bc++
		}
	case '}':
		if bc != 0 {
			bc--
		}
	case '(':
		if pc != 0 {
			pc++
		}
	case ')':
		if pc != 0 {
			pc--
		}
	case '[':
		if xc != 0 {
			xc++
		}
	case ']':
		if xc != 0 {
			xc--
		}
	}
	return bc, pc, xc
}
