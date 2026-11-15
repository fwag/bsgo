package buildconfig

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// isPerlSpace reports whether b is whitespace in the sense of Perl's \s.
func isPerlSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}

func trimLeftWS(s string) string {
	i := 0
	for i < len(s) && isPerlSpace(s[i]) {
		i++
	}
	return s[i:]
}

// splitWS mirrors Perl's split(' ', $s): leading whitespace is skipped and
// fields are separated by runs of whitespace.
func splitWS(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		for i < len(s) && isPerlSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		j := i
		for j < len(s) && !isPerlSpace(s[j]) {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

// splitWS2 mirrors Perl's split(' ', $s, 2): the second field is the
// remainder after the first whitespace run and keeps trailing whitespace.
func splitWS2(s string) []string {
	s = trimLeftWS(s)
	if s == "" {
		return nil
	}
	i := 0
	for i < len(s) && !isPerlSpace(s[i]) {
		i++
	}
	if i == len(s) {
		return []string{s}
	}
	return []string{s[:i], trimLeftWS(s[i:])}
}

// splitRe mirrors Perl's split(/re/, $s): trailing empty fields are removed.
func splitRe(re *regexp.Regexp, s string) []string {
	if s == "" {
		return nil
	}
	parts := re.Split(s, -1)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// unify removes duplicates keeping the first occurrence (Build::unify).
func unify(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func perlTrue(s string) bool { return s != "" && s != "0" }

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

var numPrefixRe = regexp.MustCompile(`^\s*[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?`)

// perlNum converts a string to a number the way Perl does: the longest
// numeric prefix is used, anything else yields zero.
func perlNum(s string) float64 {
	m := numPrefixRe.FindString(s)
	if m == "" {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(m), 64)
	if err != nil {
		return 0
	}
	return f
}

// perlInt is perlNum truncated to an integer.
func perlInt(s string) int {
	f := perlNum(s)
	if f > math.MaxInt32 {
		return math.MaxInt32
	}
	if f < math.MinInt32 {
		return math.MinInt32
	}
	return int(f)
}

// fmtNum formats a number like Perl's default stringification.
func fmtNum(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 15, 64)
}
