package buildconfig

import (
	"errors"
	"regexp"
	"strings"
)

// expandFn expands macros inside a fragment of an expression. A nil
// expandFn means that no macro expansion takes place.
type expandFn func(string) string

// Expression values follow obs-build's encoding: a leading 'v' marks a
// version string, a leading '"' a plain string, anything else is a number.

var (
	exprNumRe     = regexp.MustCompile(`^[0-9]+`)
	exprVerRe     = regexp.MustCompile(`(?s)^v"(.*?)(".*)$`)
	exprStrRe     = regexp.MustCompile(`(?s)^(".*?)(".*)$`)
	exprFuncRe    = regexp.MustCompile(`(?s)^([a-zA-Z_][a-zA-Z_0-9.:]*)\((.*)$`)
	exprIdentRe   = regexp.MustCompile(`(?s)^([a-zA-Z][a-zA-Z_0-9]*)(.*)$`)
	exprPlainRe   = regexp.MustCompile(`^[^%"]+`)
	exprBoolFalse = regexp.MustCompile(`^(?:0*$|v)`)
	exprNotNumRe  = regexp.MustCompile(`^["a-zA-Z_]`)
)

func exprBoolify(v string) bool {
	return v != `"` && !exprBoolFalse.MatchString(v)
}

func exprStringify(v string) string {
	if v != "" && (v[0] == 'v' || v[0] == '"') {
		return v[1:]
	}
	return v
}

func exprDummy(s string) string {
	if s == "" {
		return ""
	}
	return s[:1]
}

func exprVcmp(v1, v2 string, rr int) bool {
	r := verscmp(substrFrom1(v1), substrFrom1(v2))
	code := 2
	switch {
	case r < 0:
		code = 1
	case r > 0:
		code = 4
	}
	return code&rr != 0
}

func substrFrom1(s string) string {
	if s == "" {
		return ""
	}
	return s[1:]
}

// exprExpand ports expr_expand: it gathers literal text and macro
// references, expands them and returns the expanded value plus the rest.
func exprExpand(v, expr string, xp expandFn, re *regexp.Regexp) (string, string) {
	for {
		switch {
		case strings.HasPrefix(expr, "%%"):
			v += expr[:2]
			expr = expr[2:]
		case strings.HasPrefix(expr, "%"):
			m := macroEnd(expr)
			v += m
			expr = expr[len(m):]
		default:
			m := re.FindString(expr)
			if m == "" {
				return xp(v), expr
			}
			v += m
			expr = expr[len(m):]
		}
	}
}

// parseExpr ports Build::Rpm::expr. It returns the value, the unparsed rest
// and an error for malformed expressions.
func parseExpr(expr string, lev int, xp expandFn) (string, string, error) {
	v, expr, err := exprOperand(trimLeftWS(expr), xp)
	if err != nil {
		return "", expr, err
	}
	if lev >= 6 {
		return v, expr, nil
	}
	return exprOperators(v, expr, lev, xp)
}

// exprUnary parses the operand of a prefix operator.
func exprUnary(expr string, xp expandFn) (string, string, error) {
	return parseExpr(expr[1:], 6, xp)
}

func exprOperand(expr string, xp expandFn) (string, string, error) {
	t := byte(0)
	if expr != "" {
		t = expr[0]
	}
	switch t {
	case '(':
		v, rest, err := parseExpr(expr[1:], 0, xp)
		if err != nil {
			return "", rest, err
		}
		if !strings.HasPrefix(rest, ")") {
			return "", rest, errors.New("unmatched (")
		}
		return v, rest[1:], nil
	case '!':
		v, rest, err := exprUnary(expr, xp)
		if err != nil {
			return "", rest, err
		}
		return boolStr(!exprBoolify(v)), rest, nil
	case '-':
		return exprNegate(expr, xp)
	}
	return exprLiteral(expr, xp)
}

func exprNegate(expr string, xp expandFn) (string, string, error) {
	v, rest, err := exprUnary(expr, xp)
	if err != nil {
		return "", rest, err
	}
	if exprNotNumRe.MatchString(v) {
		return "", rest, errors.New("argument of - is not a number")
	}
	return fmtNum(-perlNum(v)), rest, nil
}

// exprLiteral parses numbers, strings, versions, function calls and bare
// identifiers.
func exprLiteral(expr string, xp expandFn) (string, string, error) {
	if exprNumRe.MatchString(expr) || (xp != nil && strings.HasPrefix(expr, "%")) {
		v, rest := exprNumeric(expr, xp)
		return v, rest, nil
	}
	if m := exprVerRe.FindStringSubmatch(expr); m != nil {
		return exprQuoted("v", m, xp, 2)
	}
	if m := exprStrRe.FindStringSubmatch(expr); m != nil {
		return exprQuoted("", m, xp, 1)
	}
	if m := exprFuncRe.FindStringSubmatch(expr); m != nil {
		return exprDoFunction(m[1], m[2], xp)
	}
	if m := exprIdentRe.FindStringSubmatch(expr); m != nil {
		return `"` + m[1], m[2], nil
	}
	return "", "syntax error in expression", errors.New("syntax error in expression")
}

// exprNumeric handles the numeric branch of expr(): with an expander the
// literal may be assembled from digits and macro references.
func exprNumeric(expr string, xp expandFn) (string, string) {
	if xp == nil {
		d := exprNumRe.FindString(expr)
		return fmtNum(perlNum(d)), expr[len(d):]
	}
	v, rest := exprExpand("0", expr, xp, exprNumRe)
	return fmtNum(perlNum(v)), rest
}

// exprQuoted handles string ("...") and version (v"...") literals. skip is
// the number of leading characters removed from the concatenated capture
// groups before expansion; for versions obs-build removes two, which also
// drops the first two characters of the content. This is mirrored on purpose.
func exprQuoted(prefix string, m []string, xp expandFn, skip int) (string, string, error) {
	v := prefix + m[1]
	rest := m[2]
	if prefix == "" {
		v = m[1]
	}
	if xp != nil {
		joined := m[1] + m[2]
		if skip > len(joined) {
			joined = ""
		} else {
			joined = joined[skip:]
		}
		start := prefix
		if prefix == "" {
			start = `"`
		}
		v, rest = exprExpand(start, joined, xp, exprPlainRe)
	}
	return v, strings.TrimPrefix(rest, `"`), nil
}

func exprDoFunction(name, expr string, xp expandFn) (string, string, error) {
	var args []string
	for {
		expr = trimLeftWS(expr)
		if strings.HasPrefix(expr, ")") {
			expr = expr[1:]
			break
		}
		v, rest, err := parseExpr(expr, 0, xp)
		if err != nil {
			return "", rest, err
		}
		args = append(args, exprStringify(v))
		expr = trimLeftWS(rest)
		if !strings.HasPrefix(expr, ")") && !strings.HasPrefix(expr, ",") {
			return "", "unmatched function (", errors.New("unmatched function (")
		}
		expr = strings.TrimPrefix(expr, ",")
	}
	if fn, ok := strings.CutPrefix(name, "lua:string."); ok && fn != "" {
		var werr string
		ret := luaMacro(fn, args, func(m string) {
			if werr == "" {
				werr = m
			}
		})
		if werr != "" {
			return "", werr, errors.New(werr)
		}
		return `"` + ret, expr, nil
	}
	msg := "unsupported expression function " + name
	return "", msg, errors.New(msg)
}

// binaryOp describes one infix operator.
type binaryOp struct {
	tok   string
	level int // the operator is left alone when the caller's level is >= level
	rr    int // version comparison mask (0 for arithmetic operators)
}

// exprOps lists the operators in the order obs-build tries them.
var exprOps = []binaryOp{
	{"&&", 2, 0}, {"||", 2, 0},
	{">=", 3, 6}, {">", 3, 4}, {"<=", 3, 3}, {"<", 3, 1},
	{"==", 3, 2}, {"!=", 3, 5},
	{"+", 4, 0}, {"-", 4, 0},
	{"*", 0, 0}, {"/", 0, 0},
}

func matchOp(expr string) (binaryOp, bool) {
	for _, op := range exprOps {
		if strings.HasPrefix(expr, op.tok) {
			return op, true
		}
	}
	return binaryOp{}, false
}

func exprOperators(v, expr string, lev int, xp expandFn) (string, string, error) {
	for {
		expr = trimLeftWS(expr)
		if strings.HasPrefix(expr, "?") {
			if lev > 1 {
				return v, expr, nil
			}
			var err error
			if v, expr, err = exprTernary(v, expr, xp); err != nil {
				return "", expr, err
			}
			continue
		}
		op, ok := matchOp(expr)
		if !ok {
			return exprTail(v, expr)
		}
		if op.level != 0 && lev >= op.level {
			return v, expr, nil
		}
		var err error
		if v, expr, err = exprApply(op, v, expr, xp); err != nil {
			return "", expr, err
		}
	}
}

func exprTail(v, expr string) (string, string, error) {
	if expr != "" && strings.ContainsRune("=&|", rune(expr[0])) {
		msg := "syntax error while parsing " + expr[:1] + expr[:1]
		return "", msg, errors.New(msg)
	}
	return v, expr, nil
}

func exprTernary(v, expr string, xp expandFn) (string, string, error) {
	b := exprBoolify(v)
	x1, x2 := xp, xp
	if xp != nil {
		if !b {
			x1 = exprDummy
		} else {
			x2 = exprDummy
		}
	}
	a, rest, err := parseExpr(expr[1:], 1, x1)
	if err != nil {
		return "", rest, err
	}
	if !strings.HasPrefix(rest, ":") {
		msg := "syntax error while parsing ternary in " + expr
		return "", msg, errors.New(msg)
	}
	c, rest, err := parseExpr(rest[1:], 1, x2)
	if err != nil {
		return "", rest, err
	}
	if b {
		return a, rest, nil
	}
	return c, rest, nil
}

func exprApply(op binaryOp, v, expr string, xp expandFn) (string, string, error) {
	rhsLevel := op.level
	if op.level == 0 {
		rhsLevel = 5
	}
	rhsXP := xp
	if xp != nil && (op.tok == "&&" && !exprBoolify(v) || op.tok == "||" && exprBoolify(v)) {
		rhsXP = exprDummy
	}
	v2, rest, err := parseExpr(expr[len(op.tok):], rhsLevel, rhsXP)
	if err != nil {
		return "", rest, err
	}
	res, err := exprCompute(op, v, v2)
	return res, rest, err
}

func exprCompute(op binaryOp, v, v2 string) (string, error) {
	switch op.tok {
	case "&&":
		if exprBoolify(v) {
			return v2, nil
		}
		return v, nil
	case "||":
		if exprBoolify(v) {
			return v, nil
		}
		return v2, nil
	}
	if op.rr != 0 {
		return boolStr(exprCompare(op, v, v2)), nil
	}
	return exprArith(op.tok, v, v2)
}

func exprCompare(op binaryOp, v, v2 string) bool {
	if strings.HasPrefix(v, "v") {
		return exprVcmp(v, v2, op.rr)
	}
	if strings.HasPrefix(v, `"`) {
		c := strings.Compare(v, v2)
		return cmpMatches(op.tok, c)
	}
	n1, n2 := perlNum(v), perlNum(v2)
	c := 0
	if n1 < n2 {
		c = -1
	} else if n1 > n2 {
		c = 1
	}
	return cmpMatches(op.tok, c)
}

func cmpMatches(tok string, c int) bool {
	switch tok {
	case ">=":
		return c >= 0
	case ">":
		return c > 0
	case "<=":
		return c <= 0
	case "<":
		return c < 0
	case "==":
		return c == 0
	}
	return c != 0
}

func exprArith(tok, v, v2 string) (string, error) {
	if tok == "+" && strings.HasPrefix(v, `"`) && strings.HasPrefix(v2, `"`) {
		return v + v2[1:], nil
	}
	if exprNotNumRe.MatchString(v) || exprNotNumRe.MatchString(v2) {
		return "", errors.New("argument of " + tok + " is not a number")
	}
	a, b := perlNum(v), perlNum(v2)
	switch tok {
	case "+":
		return fmtNum(a + b), nil
	case "-":
		return fmtNum(a - b), nil
	case "*":
		return fmtNum(a * b), nil
	}
	if b == 0 {
		return "", errors.New("division by zero")
	}
	return fmtNum(a / b), nil
}

// evalExpr ports do_expr: it evaluates a complete expression. Errors are
// reported through warn and yield ok == false.
func evalExpr(expr string, xp expandFn, warn func(string)) (string, bool) {
	v, rest, err := parseExpr(expr, 0, xp)
	if err != nil {
		warn(err.Error())
		return "", false
	}
	if strings.TrimSpace(rest) != "" {
		warn("syntax error in expression")
	}
	return v, true
}
