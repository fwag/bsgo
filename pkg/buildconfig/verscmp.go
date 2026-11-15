package buildconfig

import (
	"regexp"
	"strings"
)

var evrRe = regexp.MustCompile(`(?s)^(?:([0-9]+):)?(.*?)(?:-([^-]*))?$`)

func isAlnum(b byte) bool { return isDigit(b) || isAlpha(b) }

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isAlpha(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func skipSeparators(s string) string {
	i := 0
	for i < len(s) && !isAlnum(s[i]) && s[i] != '~' && s[i] != '^' {
		i++
	}
	return s[i:]
}

func takeWhile(s string, f func(byte) bool) (string, string) {
	i := 0
	for i < len(s) && f(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// compareSegment compares one numeric or alphabetic segment. It returns the
// remainders and a verdict; done is true when the verdict is final.
func compareSegment(s1, s2 string) (r1, r2 string, verdict int, done bool) {
	if isDigit(s1[0]) {
		x1, n1 := takeWhile(s1, isDigit)
		x2, n2 := takeWhile(s2, isDigit)
		if x2 == "" {
			return n1, n2, 1, true
		}
		x1 = strings.TrimLeft(x1, "0")
		x2 = strings.TrimLeft(x2, "0")
		v := len(x1) - len(x2)
		if v == 0 {
			v = strings.Compare(x1, x2)
		}
		return n1, n2, v, v != 0
	}
	x1, n1 := takeWhile(s1, isAlpha)
	x2, n2 := takeWhile(s2, isAlpha)
	if x1 == "" || x2 == "" {
		return n1, n2, -1, true
	}
	v := strings.Compare(x1, x2)
	return n1, n2, v, v != 0
}

// tildeCaret handles the ~ and ^ ordering rules. handled is false when
// neither string starts with such a marker.
func tildeCaret(s1, s2 string) (r1, r2 string, verdict int, done, handled bool) {
	switch {
	case strings.HasPrefix(s1, "~"):
		if strings.HasPrefix(s2, "~") {
			return s1[1:], s2[1:], 0, false, true
		}
		return s1, s2, -1, true, true
	case strings.HasPrefix(s2, "~"):
		return s1, s2, 1, true, true
	case strings.HasPrefix(s1, "^"):
		if strings.HasPrefix(s2, "^") {
			return s1[1:], s2[1:], 0, false, true
		}
		return s1[1:], s2, bToInt(s2 == "", 1, -1), true, true
	case strings.HasPrefix(s2, "^"):
		return s1, s2, bToInt(s1 == "", -1, 1), true, true
	}
	return s1, s2, 0, false, false
}

func bToInt(b bool, t, f int) int {
	if b {
		return t
	}
	return f
}

// verscmpPart ports Build::Rpm::verscmp_part.
func verscmpPart(s1, s2 string) int {
	if s1 == s2 {
		return 0
	}
	for {
		s1 = skipSeparators(s1)
		s2 = skipSeparators(s2)
		r1, r2, v, done, handled := tildeCaret(s1, s2)
		if done {
			return v
		}
		if handled {
			s1, s2 = r1, r2
			continue
		}
		if s1 == "" {
			return bToInt(s2 == "", 0, -1)
		}
		if s2 == "" {
			return 1
		}
		s1, s2, v, done = compareSegment(s1, s2)
		if done {
			return bToInt(v > 0, 1, -1)
		}
	}
}

// verscmp ports Build::Rpm::verscmp (without the dtest flag).
func verscmp(s1, s2 string) int {
	if s1 == s2 {
		return 0
	}
	m1 := evrRe.FindStringSubmatch(s1)
	m2 := evrRe.FindStringSubmatch(s2)
	e1, e2 := defaultEpoch(m1[1]), defaultEpoch(m2[1])
	if e1 != e2 {
		if r := verscmpPart(e1, e2); r != 0 {
			return r
		}
	}
	if m1[2] != m2[2] {
		if r := verscmpPart(m1[2], m2[2]); r != 0 {
			return r
		}
	}
	if m1[3] != m2[3] {
		return verscmpPart(m1[3], m2[3])
	}
	return 0
}

func defaultEpoch(e string) string {
	if e == "" {
		return "0"
	}
	return e
}
