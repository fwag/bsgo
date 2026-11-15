package buildconfig

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	whitespaceRunRe = regexp.MustCompile(`[ \t]+`)
	luaPercentRe    = regexp.MustCompile(`\\%([%ds])`)
	luaNames        = map[string]bool{
		"gsub": true, "len": true, "lower": true, "upper": true,
		"rep": true, "reverse": true, "sub": true,
	}
)

// builtin ports Build::Rpm::builtinmacro and dispatches to the lua-style
// string macros.
func (e *macroEngine) builtin(name string, args []string, tries int) string {
	if len(args) == 0 {
		args = []string{""}
	}
	if luaNames[name] {
		return luaMacro(name, args, e.warn)
	}
	switch name {
	case "expr":
		v, _ := evalExpr(args[0], nil, e.warn)
		return exprStringify(v)
	case "quote":
		return e.quoteArgs(args)
	case "bcond_override_default":
		e.macros["bcond_override_default_"+args[0]] = argAt(args, 1)
		return ""
	case "bcond", "bcond_with", "bcond_without":
		e.bcond(name, args, tries)
		return ""
	case "with", "without":
		return boolStr(e.has("with_"+args[0]) != (name == "without"))
	case "defined", "undefined":
		return boolStr(e.has(args[0]) != (name == "undefined"))
	}
	return e.pathMacro(name, args[0])
}

func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// pathMacro implements the pure string builtins.
func (e *macroEngine) pathMacro(name, arg string) string {
	switch name {
	case "basename":
		if i := strings.LastIndexByte(arg, '/'); i >= 0 {
			return arg[i+1:]
		}
		return arg
	case "dirname":
		if i := strings.LastIndexByte(arg, '/'); i >= 0 {
			return arg[:i]
		}
		return arg
	case "suffix":
		if i := strings.LastIndexByte(arg, '.'); i >= 0 {
			return arg[i+1:]
		}
		return ""
	case "span":
		return arg
	case "shrink":
		arg = whitespaceRunRe.ReplaceAllString(arg, " ")
		return strings.TrimSuffix(strings.TrimPrefix(arg, " "), " ")
	}
	e.warn("unsupported builtin macro %" + name)
	return ""
}

func (e *macroEngine) quoteArgs(args []string) string {
	if !e.quote {
		return strings.Join(args, " ")
	}
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" {
			a = "\x1f00"
		}
		var b strings.Builder
		for j := 0; j < len(a); j++ {
			if a[j] == ' ' || a[j] == '\t' {
				fmt.Fprintf(&b, "\x1f%02X", a[j])
				continue
			}
			b.WriteByte(a[j])
		}
		out[i] = b.String()
	}
	return strings.Join(out, " ")
}

// bcond ports the %bcond, %bcond_with and %bcond_without handling.
func (e *macroEngine) bcond(name string, args []string, tries int) {
	def := argAt(args, 1)
	switch name {
	case "bcond_with":
		def = "0"
	case "bcond_without":
		def = "1"
	}
	override, ok := e.macros["bcond_override_default_"+args[0]]
	if ok && strings.Contains(override, "%") {
		override = e.expand(override, tries)
	}
	if !ok || override == "" {
		override = def
	}
	if !perlTrue(override) && e.has("_with_"+args[0]) {
		e.macros["with_"+args[0]] = "1"
	}
	if perlTrue(override) && !e.has("_without_"+args[0]) {
		e.macros["with_"+args[0]] = "1"
	}
}

// luaMacro ports Build::Rpm::luamacro (the %{lower:..}, %{sub:..} family).
func luaMacro(name string, args []string, warn func(string)) string {
	if len(args) == 0 {
		args = []string{""}
	}
	switch name {
	case "lower":
		return strings.ToLower(args[0])
	case "upper":
		return strings.ToUpper(args[0])
	case "len":
		return strconv.Itoa(len(args[0]))
	case "reverse":
		return reverseString(args[0])
	case "rep":
		return strings.Repeat(args[0], max(perlInt(argAt(args, 1)), 0))
	case "sub":
		return luaSub(args)
	case "gsub":
		return luaGsub(args, warn)
	}
	warn("unsupported lua macro " + name)
	return ""
}

func reverseString(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// luaIndex normalizes a 1-based, possibly negative, Lua index to a 0-based one.
func luaIndex(arg string, length int) int {
	n := perlInt(arg)
	if n > 0 {
		n--
	}
	if n < 0 {
		n += length
	}
	return max(n, 0)
}

func luaSub(args []string) string {
	for len(args) < 2 {
		args = append(args, "1")
	}
	for len(args) < 3 {
		args = append(args, "-1")
	}
	s := args[0]
	from, to := luaIndex(args[1], len(s)), luaIndex(args[2], len(s))
	if from > to || from >= len(s) {
		return ""
	}
	return s[from:min(to+1, len(s))]
}

// luaPattern ports luapattern: a very small subset of Lua patterns mapped to
// a regular expression. Like the original, each special sequence is only
// translated at its first occurrence.
func luaPattern(pat string) string {
	p := quotemeta(pat)
	if strings.HasPrefix(p, `\^`) {
		p = "^" + p[2:]
	}
	for _, r := range [][2]string{{`\$`, `$`}, {`\.`, `.`}, {`\*`, `*`}, {`\-`, `*?`}, {`\?`, `?`}, {`\+`, `+`}} {
		p = strings.Replace(p, r[0], r[1], 1)
	}
	return luaPercentRe.ReplaceAllString(p, `\$1`)
}

// quotemeta mirrors Perl's quotemeta for ASCII input.
func quotemeta(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x80 && !isAlnum(c) && c != '_' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

func luaGsub(args []string, warn func(string)) string {
	if len(args) < 3 {
		return ""
	}
	re, err := regexp.Compile(luaPattern(args[1]))
	if err != nil {
		warn("gsub macro: " + err.Error())
		return ""
	}
	limit := -1
	if len(args) > 3 {
		limit = max(perlInt(args[3]), 0)
	}
	return replaceLimited(re, args[0], args[2], limit)
}

// replaceLimited replaces the first limit matches (all when limit < 0; none when 0) with
// the literal text repl.
func replaceLimited(re *regexp.Regexp, s, repl string, limit int) string {
	matches := re.FindAllStringIndex(s, limit)
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		b.WriteString(repl)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}
