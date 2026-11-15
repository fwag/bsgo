package buildconfig

import (
	"regexp"
	"strconv"
	"strings"
)

// stdMacros mirrors $std_macros in Build.pm (synced with rpm 4.20.0).
const stdMacros = `
%define nil
%define ix86    i386 i486 i586 i686 pentium3 pentium4 athlon geode
%define x86_64  x86_64 x86_64_v2 x86_64_v3 x86_64_v4 amd64 em64t
%define arm32   armv3l armv4b armv4l armv4tl armv5tl armv5tel armv5tejl armv6l armv6hl armv7l armv7hl armv7hnl armv8l armv8hl armv8hnl armv8hcnl
%define arm     %{arm32}
%define arml    armv3l armv4l armv5l armv5tel armv6l armv6hl armv7l armv7hl armv7hnl
%define armb    armv4b armv5b armv5teb
%define arm64   aarch64
%define mips32  mips mipsel mipsr6 mipsr6el
%define mips64  mips64 mips64el mips64r6 mips64r6el
%define mipseb  mips mipsr6 mips64 mips64r6
%define mipsel  mipsel mipsr6el mips64el mips64r6el
%define mips    %{mips32} %{mips64}
%define loongarch64 loongarch64
%define sparc   sparc sparcv8 sparcv9 sparcv9v sparc64 sparc64v
%define alpha   alpha alphaev56 alphaev6 alphaev67
%define power64 ppc64 ppc64p7 ppc64le
%define riscv32 riscv32
%define riscv64 riscv64
%define riscv128 riscv128
%define riscv   %{riscv32} %{riscv64} %{riscv128}
`

const maxMacroTries = 1000

var (
	macroRe      = regexp.MustCompile(`(?s)^(.*?)%(\{([^}]+)\}|[?!]*[0-9a-zA-Z_]+|%|\*\*?|#|\(|\[)`)
	macroLineRe  = regexp.MustCompile(`(?s)^%define\s*([0-9a-zA-Z_]+)(?:\(([^)]*)\))?\s*(.*?)\n?$`)
	macroDefRe   = regexp.MustCompile(`(?s)^\s*([0-9a-zA-Z_]+)(?:\(([^)]*)\))?\s*(.*?)\n?$`)
	macroNameRe  = regexp.MustCompile(`(?s)^([^\s:]+)([\s:])(.*)$`)
	macroArgRe   = regexp.MustCompile(`^\s*([^\n]*)`)
	macroQuoteRe = regexp.MustCompile(`\x1f([0-9A-F]{2})`)
	macroEndRe   = regexp.MustCompile(`^(%[?!]*-?[a-zA-Z0-9_]*(?:\*|\*\*|#)?)`)
)

// builtinMacros lists the macro names handled by the engine itself.
var builtinMacros = map[string]bool{
	"expand": true, "defined": true, "undefined": true, "with": true,
	"without": true, "bcond": true, "bcond_with": true, "bcond_without": true,
	"bcond_override_default": true, "expr": true, "basename": true,
	"dirname": true, "shrink": true, "suffix": true, "load": true,
	"span": true, "quote": true, "gsub": true, "len": true, "lower": true,
	"upper": true, "rep": true, "reverse": true, "sub": true,
}

// macroEngine is a port of Build::Rpm::expandmacros and its macro tables.
type macroEngine struct {
	macros map[string]string // macro name to body
	args   map[string]string // parametric macros: name to option spec
	warn   func(string)
	quote  bool
}

func newMacroEngine(defs []string, warn func(string)) *macroEngine {
	e := &macroEngine{
		macros: map[string]string{},
		args:   map[string]string{},
		warn:   warn,
	}
	for _, line := range defs {
		m := macroLineRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		e.setFrom(line, m)
	}
	return e
}

// setFrom stores a macro from the submatch indices of macroLineRe or
// macroDefRe: name is group 1, the optional option spec group 2, body group 3.
func (e *macroEngine) setFrom(s string, m []int) {
	name := s[m[2]:m[3]]
	if m[4] >= 0 {
		e.args[name] = s[m[4]:m[5]]
	} else {
		delete(e.args, name)
	}
	e.macros[name] = s[m[6]:m[7]]
}

// macroEnd ports Build::Rpm::macroend: it returns the text of the macro
// reference at the start of expr.
func macroEnd(expr string) string {
	if len(expr) >= 2 && expr[0] == '%' && strings.IndexByte("({[", expr[1]) >= 0 {
		return bracketEnd(expr)
	}
	return macroEndRe.FindString(expr)
}

func bracketEnd(expr string) string {
	open := expr[1]
	if open == '[' {
		// obs-build builds the scan pattern /^(.*?)([$o$c\\])/ which, for
		// '[' and ']', degenerates to the sequence "[\]" and never matches
		// in practice. The reference therefore returns the whole remaining
		// text, so a %[...] reference swallows the rest of the line. Mirror it.
		return expr
	}
	closer := map[byte]byte{'(': ')', '{': '}', '[': ']'}[open]
	m := expr[:2]
	rest := expr[2:]
	cnt := 1
	for {
		i := strings.IndexAny(rest, string([]byte{open, closer, '\\'}))
		if i < 0 {
			return m + rest
		}
		m += rest[:i+1]
		ch := rest[i]
		rest = rest[i+1:]
		switch ch {
		case '\\':
			if rest != "" {
				m += rest[:1]
				rest = rest[1:]
			}
		case open:
			cnt++
		default:
			cnt--
			if cnt == 0 {
				return m
			}
		}
	}
}

type stackItem struct {
	line  string
	opt   map[string]string
	frame bool
}

type expState struct {
	out   strings.Builder
	line  string
	tries int
	stack []stackItem
	opt   map[string]string
}

// macroRef describes one parsed %-reference.
type macroRef struct {
	orig string
	name string
	data *string
	alt  *string
	test int // -1 for %{!?x}, 1 for %{?x}, 0 otherwise
}

// expand ports expandmacros.
func (e *macroEngine) expand(line string, tries int) string {
	st := &expState{line: line, tries: tries, opt: map[string]string{}}
	for {
		for e.step(st) {
		}
		st.out.WriteString(st.line)
		if len(st.stack) == 0 {
			return st.out.String()
		}
		top := st.stack[len(st.stack)-1]
		st.stack = st.stack[:len(st.stack)-1]
		if top.frame {
			st.opt = e.adapt(st.opt, top.opt)
		}
		st.line = top.line
	}
}

// adapt ports adaptmacros: remove the old option macros, install the new.
func (e *macroEngine) adapt(oldOpt, newOpt map[string]string) map[string]string {
	for k := range oldOpt {
		delete(e.macros, k)
	}
	for k, v := range newOpt {
		e.macros[k] = v
	}
	return newOpt
}

// step consumes one macro reference. It returns false when expansion of the
// current line is finished.
func (e *macroEngine) step(st *expState) bool {
	m := macroRe.FindStringSubmatchIndex(st.line)
	if m == nil {
		return false
	}
	over := st.tries > maxMacroTries
	st.tries++
	if over {
		e.warn("macro too deeply nested")
		st.line = "MACRO"
		return false
	}
	st.out.WriteString(st.line[:m[3]])
	rest := st.line[m[1]:]
	if st.line[m[4]:m[5]] == "%" {
		st.out.WriteByte('%')
		st.line = rest
		return true
	}
	g3 := ""
	if m[6] >= 0 {
		g3 = st.line[m[6]:m[7]]
	}
	ref := parseRef(st.line[m[4]:m[5]], g3, m[6] >= 0, &rest)
	st.line = rest
	return e.dispatch(st, ref)
}

func parseRef(g2, g3 string, hasG3 bool, rest *string) *macroRef {
	r := &macroRef{orig: g2, name: g2}
	if hasG3 {
		parseBraced(r, g3, rest)
	}
	switch {
	case strings.HasPrefix(r.name, "!?") || strings.HasPrefix(r.name, "?!"):
		r.test = -1
	case strings.HasPrefix(r.name, "-") || strings.HasPrefix(r.name, "?"):
		r.test = 1
	}
	r.name = strings.TrimLeft(r.name, "!?")
	return r
}

// parseBraced handles the %{name}, %{name:alt} and %{name data} forms.
func parseBraced(r *macroRef, g3 string, rest *string) {
	r.name = g3
	if strings.ContainsAny(r.name, "{\\") {
		full := "%" + r.orig + *rest
		me := macroEnd(full)
		*rest = full[len(me):]
		r.orig = me[1:]
		r.name = strings.TrimSuffix(strings.TrimPrefix(me, "%{"), "}")
	}
	empty := ""
	r.data = &empty
	m := macroNameRe.FindStringSubmatch(r.name)
	if m == nil {
		return
	}
	r.name = m[1]
	text := m[3]
	if m[2] == ":" {
		r.alt = &text
	} else {
		r.data = &text
	}
}

func (e *macroEngine) dispatch(st *expState, r *macroRef) bool {
	switch {
	case r.name == "(":
		e.warn("cannot expand %(...)")
		st.line = "MACRO"
		return false
	case r.name == "[":
		e.expandExpr(st)
	case r.name == "define" || r.name == "global":
		e.define(st, r)
	case r.name == "dnl":
		if i := strings.IndexByte(st.line, '\n'); i >= 0 {
			st.line = st.line[i+1:]
		} else {
			st.line = ""
		}
	case !builtinMacros[r.name] && !e.has(r.name):
		e.unknown(st, r)
	default:
		return e.existing(st, r)
	}
	return true
}

func (e *macroEngine) has(name string) bool {
	_, ok := e.macros[name]
	return ok
}

// expandExpr handles %[ expr ].
func (e *macroEngine) expandExpr(st *expState) {
	alt := macroEnd("%[" + st.line)
	st.line = st.line[len(alt)-2:]
	alt = strings.TrimSuffix(strings.TrimPrefix(alt, "%["), "]")
	tries := st.tries
	xp := func(s string) string { return e.expand(s, tries) }
	v, _ := evalExpr(alt, xp, e.warn)
	st.out.WriteString(exprStringify(v))
}

func (e *macroEngine) define(st *expState, r *macroRef) {
	var data string
	if r.data != nil {
		data = *r.data
	} else {
		data = st.line
		st.line = ""
	}
	m := macroDefRe.FindStringSubmatchIndex(data)
	if m == nil {
		return
	}
	if r.name == "global" {
		data = data[:m[6]] + e.expand(data[m[6]:m[7]], st.tries)
		if m = macroDefRe.FindStringSubmatchIndex(data); m == nil {
			return
		}
	}
	e.setFrom(data, m)
}

// emitOrPush appends text to the output, or schedules it for re-expansion
// when it still contains macro references.
func (e *macroEngine) emitOrPush(st *expState, text string) {
	if !strings.Contains(text, "%") {
		st.out.WriteString(text)
		return
	}
	if st.line != "" {
		st.stack = append(st.stack, stackItem{line: st.line})
	}
	st.line = text
}

func (e *macroEngine) unknown(st *expState, r *macroRef) {
	switch {
	case r.test == 0:
		st.out.WriteString("%" + r.orig)
	case r.test < 0 && r.alt != nil:
		e.emitOrPush(st, *r.alt)
	}
}

func (e *macroEngine) existing(st *expState, r *macroRef) bool {
	if r.test < 0 {
		return true
	}
	if _, parametric := e.args[r.name]; !builtinMacros[r.name] && !parametric {
		alt := e.macros[r.name]
		if r.test != 0 && r.alt != nil {
			alt = *r.alt
		}
		e.emitOrPush(st, alt)
		return true
	}
	args := e.collectArgs(st, r)
	return e.invoke(st, r.name, args)
}

func (e *macroEngine) collectArgs(st *expState, r *macroRef) []string {
	if r.alt != nil {
		return []string{e.expand(*r.alt, st.tries)}
	}
	var data string
	if r.data != nil {
		data = *r.data
	} else {
		data = macroArgRe.FindStringSubmatch(st.line)[1]
		st.line = ""
	}
	saved := e.quote
	e.quote = true
	data = e.expand(data, st.tries)
	e.quote = saved
	args := splitWS(data)
	for i, a := range args {
		args[i] = macroQuoteRe.ReplaceAllStringFunc(a, unquoteByte)
	}
	return args
}

func unquoteByte(m string) string {
	h := m[1:]
	if h == "00" {
		return ""
	}
	n, err := strconv.ParseUint(h, 16, 8)
	if err != nil {
		return ""
	}
	return string([]byte{byte(n)})
}

func (e *macroEngine) invoke(st *expState, name string, args []string) bool {
	switch {
	case name == "expand":
		text := ""
		if len(args) > 0 {
			text = args[0]
		}
		e.emitOrPush(st, text)
	case builtinMacros[name]:
		st.out.WriteString(e.builtin(name, args, st.tries))
	default:
		st.stack = append(st.stack, stackItem{line: st.line, opt: st.opt, frame: true})
		st.line = e.macros[name]
		st.opt = e.adapt(st.opt, grabArgs(name, e.args[name], args))
	}
	return true
}

// grabArgs ports Build::Rpm::grabargs: it builds the %1, %*, %-x ... macros
// for one invocation of a parametric macro.
func grabArgs(name, getopt string, args []string) map[string]string {
	m := map[string]string{"0": name, "**": strings.Join(args, " ")}
	takesArg := optionsWithArgs(getopt)
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		o := args[0][1:]
		args = args[1:]
		if o == "-" {
			break
		}
		args = parseOptionCluster(m, takesArg, o, args)
	}
	m["#"] = strconv.Itoa(len(args))
	for i, a := range args {
		m[strconv.Itoa(i+1)] = a
	}
	m["*"] = strings.Join(args, " ")
	for k, v := range m {
		m[k] = strings.Replace(v, "%", "%%", 1)
	}
	return m
}

func optionsWithArgs(getopt string) map[byte]bool {
	res := map[byte]bool{}
	for i := 0; i < len(getopt); i++ {
		if i+1 < len(getopt) && getopt[i+1] == ':' {
			res[getopt[i]] = true
		}
		for i+1 < len(getopt) && getopt[i+1] == ':' {
			i++
		}
	}
	return res
}

func parseOptionCluster(m map[string]string, takesArg map[byte]bool, o string, args []string) []string {
	for o != "" {
		c := o[0]
		rest := o[1:]
		if takesArg[c] {
			arg := rest
			if arg == "" && len(args) > 0 {
				arg = args[0]
				args = args[1:]
			}
			m["-"+string(c)] = "-" + string(c) + " " + arg
			m["-"+string(c)+"*"] = arg
			return args
		}
		m["-"+string(c)] = "-" + string(c)
		o = rest
	}
	return args
}
