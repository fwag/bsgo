package obsimport

import (
	"sort"
	"strings"
)

// Report describes what a conversion did. It is the machine-readable result
// of "bsgo project convert-obs --json".
type Report struct {
	Project string `json:"project"`
	// Targets lists the repositories taken from _meta, sorted.
	Targets []string `json:"targets"`
	// StructuredStatements counts statements stored in structured fields
	// (defines, directives, Macros: blocks), per destination copy.
	StructuredStatements int `json:"structured_statements"`
	// RawFragments counts verbatim fragments kept in raw_config.
	RawFragments int `json:"raw_fragments"`
	// RawLines counts the physical lines in those fragments.
	RawLines int `json:"raw_lines"`
	// OrderMoves counts statements moved to the public raw_config only to keep
	// their relative order (a subset of RawFragments).
	OrderMoves int `json:"order_moves"`
	// RawByReason groups RawFragments by reason.
	RawByReason map[string]int `json:"raw_by_reason"`
	// Raw lists every raw fragment in source order.
	Raw []RawEntry `json:"raw"`
	// DroppedComments counts comment lines and empty Macros: blocks that have no
	// structured representation.
	DroppedComments int `json:"dropped_comments"`
	// DroppedLineMarkers counts "#!!line" markers.
	DroppedLineMarkers int `json:"dropped_line_markers"`
}

// RawEntry describes one verbatim fragment.
type RawEntry struct {
	// Scope is the raw_config field that holds the fragment.
	Scope string `json:"scope"`
	// Line is the first source line of the fragment.
	Line   int    `json:"line"`
	Lines  int    `json:"lines"`
	Reason string `json:"reason"`
	// Excerpt is the first line of the fragment.
	Excerpt string `json:"excerpt"`
}

func newReport(meta *Meta, c *classifier, a *assembler) Report {
	r := Report{
		Project:              meta.Name,
		StructuredStatements: a.structured,
		RawByReason:          map[string]int{},
		DroppedComments:      c.comments,
		DroppedLineMarkers:   c.markers,
		Targets:              []string{},
	}
	for _, repo := range meta.Repositories {
		r.Targets = append(r.Targets, repo.Name)
	}
	sort.Strings(r.Targets)
	for _, e := range c.events {
		if e.demoted || e.kind == evRaw {
			r.addRaw(e)
		}
	}
	return r
}

func (r *Report) addRaw(e *event) {
	reason := e.reason
	scope := e.dests[0].String()
	if len(e.dests) > 1 {
		names := make([]string, 0, len(e.dests))
		for _, d := range e.dests {
			names = append(names, d.String())
		}
		scope = strings.Join(names, ",")
	}
	if e.demoted {
		reason = "order: moved with a conflicting statement"
		scope = "public_config"
		r.OrderMoves++
	}
	lines := strings.Count(e.raw, "\n") + 1
	r.RawFragments++
	r.RawLines += lines
	r.RawByReason[groupReason(reason)]++
	r.Raw = append(r.Raw, RawEntry{
		Scope: scope, Line: e.line, Lines: lines, Reason: detail(e, reason),
		Excerpt: firstLine(e.raw),
	})
}

func detail(e *event, reason string) string {
	if e.demoted {
		return e.demotedBy
	}
	return reason
}

// groupReason strips the variable tail of a reason so reasons can be counted.
func groupReason(reason string) string {
	if i := strings.Index(reason, ": "); i > 0 && strings.HasPrefix(reason, "conditional has no structured scope") {
		return reason[:i]
	}
	if strings.HasPrefix(reason, "repository ") {
		return "repository is not in _meta"
	}
	if strings.HasPrefix(reason, "directive ") {
		return "directive has no structured field"
	}
	return reason
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(line)
	if len(line) > 100 {
		line = line[:100]
	}
	return line
}
