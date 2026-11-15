package obsimport

import (
	"fmt"
	"strings"
)

// The manifest renders scopes in a fixed order (see buildconfig.Render), and
// within a scope defines, directives, raw_config and Macros. The prjconf
// source order is arbitrary. Two statements may only swap places if they do
// not touch the same state. resolveOrder finds every pair that would be
// inverted by the fixed order and moves both statements to the public
// raw_config, where source order is kept verbatim, until no inversion is left.

// conflictsBefore returns, for every event, the earlier events it conflicts
// with: both touch the same directive or macro and at least one writes it.
func conflictsBefore(evs []*event) [][]int {
	writers := map[string][]int{}
	readers := map[string][]int{}
	var barrier, dynamic, defWriters []int
	adj := make([][]int, len(evs))
	mark := make([]int, len(evs))
	for j, e := range evs {
		stamp := j + 1
		add := func(list []int) {
			for _, i := range list {
				if mark[i] != stamp {
					mark[i] = stamp
					adj[j] = append(adj[j], i)
				}
			}
		}
		for _, k := range e.writes {
			add(writers[k])
			add(readers[k])
			if k == "*" {
				for i := 0; i < j; i++ {
					add([]int{i})
				}
			}
			if strings.HasPrefix(k, "m:") {
				add(dynamic)
			}
		}
		for _, k := range e.reads {
			add(writers[k])
			if k == "*" {
				add(defWriters)
			}
		}
		add(barrier)
		for _, k := range e.writes {
			writers[k] = append(writers[k], j)
			if k == "*" {
				barrier = append(barrier, j)
			}
			if strings.HasPrefix(k, "m:") {
				defWriters = append(defWriters, j)
			}
		}
		for _, k := range e.reads {
			readers[k] = append(readers[k], j)
			if k == "*" {
				dynamic = append(dynamic, j)
			}
		}
	}
	return adj
}

// inverted reports whether the fixed render order would place the earlier
// event i after the later event j in some configuration where both apply.
func inverted(i, j *event) bool {
	for _, di := range i.effectiveDests() {
		for _, dj := range j.effectiveDests() {
			if di.compatible(dj) && i.rank(di) > j.rank(dj) {
				return true
			}
		}
	}
	return false
}

func (e *event) demote(by *event) {
	if e.inPublicRaw() {
		return
	}
	e.demoted = true
	e.demotedBy = fmt.Sprintf("order: conflicts with the statement at line %d", by.line)
}

func resolveOrder(evs []*event) {
	adj := conflictsBefore(evs)
	for changed := true; changed; {
		changed = false
		for j, ej := range evs {
			for _, i := range adj[j] {
				if !inverted(evs[i], ej) {
					continue
				}
				before := evs[i].inPublicRaw() && ej.inPublicRaw()
				evs[i].demote(ej)
				ej.demote(evs[i])
				changed = changed || !before
			}
		}
	}
}
