package obsimport

import (
	"regexp"
	"strings"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

var (
	defineRe  = regexp.MustCompile(`^\s*%define\s+(` + nameRe + `)(?:\s+(\S.*?))?\s*$`)
	keywordLn = regexp.MustCompile(`^\s*([A-Za-z]+):\s*(.*?)\s*$`)
	commentRe = regexp.MustCompile(`^\s*#`)
	lineMark  = regexp.MustCompile(`^#!!line\b`)
)

// classifier turns tree nodes into events and counts what it drops.
type classifier struct {
	meta   *Meta
	events []*event
	// seen records single-valued fields that already hold a value, so that a
	// second occurrence stays verbatim instead of being collapsed.
	seen     map[string]bool
	comments int
	markers  int
}

func newClassifier(meta *Meta) *classifier {
	return &classifier{meta: meta, seen: map[string]bool{}}
}

// walk converts nodes under the given chain of mapped guards.
func (c *classifier) walk(nodes []treeNode, gs []guard) {
	for _, n := range nodes {
		if n.cond != nil {
			c.walkCond(n.cond, gs)
			continue
		}
		c.leaf(n.item, gs)
	}
}

func (c *classifier) walkCond(cn *condNode, gs []guard) {
	g, reason := c.guardOf(cn)
	if g == nil {
		c.rawEvent(cn.head.Line, cn.raw, reason, gs)
		return
	}
	next := append(append([]guard{}, gs...), *g)
	if _, ok := destsFor(next); !ok {
		c.rawEvent(cn.head.Line, cn.raw, "conditional nesting has no structured scope", gs)
		return
	}
	c.walk(cn.body, next)
}

func (c *classifier) add(e *event, gs []guard) {
	e.dests, _ = destsFor(gs)
	for _, g := range gs {
		e.guards = append(e.guards, g.cond)
	}
	c.events = append(c.events, e)
}

func (c *classifier) rawEvent(line int, raw, reason string, gs []guard) {
	e := &event{kind: evRaw, line: line, raw: raw, reason: reason}
	e.analyze(raw)
	c.add(e, gs)
}

// barrierEvent is a raw line of unknown effect: nothing may be reordered
// across it.
func (c *classifier) barrierEvent(it *buildconfig.Item, gs []guard) {
	e := &event{kind: evRaw, line: it.Line, raw: it.Raw, reason: "line is neither a %define nor a known directive"}
	e.analyze(it.Raw)
	e.writes = append(e.writes, "*")
	c.add(e, gs)
}

func (c *classifier) leaf(it *buildconfig.Item, gs []guard) {
	switch {
	case it.Macros:
		c.macros(it, gs)
	case strings.TrimSpace(it.Text) == "":
	case lineMark.MatchString(it.Text):
		c.markers++
	case commentRe.MatchString(it.Text) && !strings.HasPrefix(it.Text, "#!"):
		c.comments++
	default:
		c.statement(it, gs)
	}
}

func (c *classifier) macros(it *buildconfig.Item, gs []guard) {
	if it.Body == "" {
		c.comments++
		return
	}
	if strings.HasPrefix(it.Body, "!\n") {
		c.rawEvent(it.Line, it.Raw, "Macros: block starting with '!' resets earlier macros", gs)
		return
	}
	e := &event{kind: evMacros, line: it.Line, raw: it.Raw, body: it.Body, writes: []string{"d:macros"}}
	e.apply = func(cfg *manifest.ProjectConfig) { cfg.Macros += it.Body }
	c.add(e, gs)
}

func (c *classifier) statement(it *buildconfig.Item, gs []guard) {
	if endifRe.MatchString(it.Text) || elseLikeRe.MatchString(it.Text) {
		c.rawEvent(it.Line, it.Raw, "conditional keyword without matching %if", gs)
		return
	}
	if m := defineRe.FindStringSubmatch(it.Text); m != nil && !strings.Contains(it.Text, "\n") {
		c.define(it, m[1], m[2], gs)
		return
	}
	if m := keywordLn.FindStringSubmatch(it.Text); m != nil && !strings.Contains(it.Text, "\n") {
		c.directive(it, strings.ToLower(m[1]), m[2], gs)
		return
	}
	c.barrierEvent(it, gs)
}

func (c *classifier) define(it *buildconfig.Item, name, value string, gs []guard) {
	e := &event{kind: evDefine, line: it.Line, raw: it.Raw, writes: []string{"m:" + name}}
	e.reads = refKeys(value)
	e.apply = func(cfg *manifest.ProjectConfig) {
		cfg.Defines = append(cfg.Defines, manifest.Define{Name: name, Value: value})
	}
	c.add(e, gs)
}

func (c *classifier) directive(it *buildconfig.Item, kw, args string, gs []guard) {
	if kw == "fromscratch" {
		c.fromScratch(it, gs)
		return
	}
	spec, ok := directiveSpecs[kw]
	if !ok {
		c.rawEvent(it.Line, it.Raw, "directive "+kw+" has no structured field", gs)
		return
	}
	apply, key, reason := spec(args)
	if apply == nil {
		c.rawEvent(it.Line, it.Raw, reason, gs)
		return
	}
	if c.duplicate(kw, key, gs) {
		c.rawEvent(it.Line, it.Raw, "second "+kw+" for the same scope and key", gs)
		return
	}
	e := &event{kind: evDirective, line: it.Line, raw: it.Raw, apply: apply}
	e.writes, e.reads = effects(kw, args)
	e.reads = append(e.reads, refKeys(args)...)
	c.add(e, gs)
}

// duplicate reports whether a single-valued field of the same key was already
// set in one of the destinations, and records it otherwise.
func (c *classifier) duplicate(kw, key string, gs []guard) bool {
	if key == "" {
		return false
	}
	ds, _ := destsFor(gs)
	dup := false
	for _, d := range ds {
		k := d.String() + "|" + kw + "|" + key
		dup = dup || c.seen[k]
		c.seen[k] = true
	}
	return dup
}

func (c *classifier) fromScratch(it *buildconfig.Item, gs []guard) {
	if len(gs) > 0 || c.firstContent() {
		c.rawEvent(it.Line, it.Raw, "FromScratch is not the first statement of the public scope", gs)
		return
	}
	e := &event{kind: evDirective, line: it.Line, raw: it.Raw, writes: []string{"d:fromscratch"}}
	e.apply = func(cfg *manifest.ProjectConfig) { cfg.FromScratch = true }
	c.add(e, gs)
}

// firstContent reports whether an event was recorded before this statement.
// Blank lines and comments produce no event.
func (c *classifier) firstContent() bool { return len(c.events) > 0 }
