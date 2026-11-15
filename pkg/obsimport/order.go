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
	x := &conflictIndex{
		writers: map[string][]int{}, readers: map[string][]int{},
		adj: make([][]int, len(evs)), mark: make([]int, len(evs)),
	}
	for j, e := range evs {
		x.collect(j, e)
		x.record(j, e)
	}
	return x.adj
}

// conflictIndex accumulates, in source order, who wrote and read which key.
// The key "*" is special: a write to it is a barrier that conflicts with every
// other statement, and a read of it (dynamic macros) conflicts with every
// macro definition.
type conflictIndex struct {
	writers, readers map[string][]int
	barrier          []int
	dynamic          []int
	defWriters       []int
	adj              [][]int
	mark             []int
}

func isMacroKey(k string) bool { return strings.HasPrefix(k, "m:") }

// add records the conflicts of event j with the given earlier events once.
func (x *conflictIndex) add(j int, earlier []int) {
	for _, i := range earlier {
		if x.mark[i] != j+1 {
			x.mark[i] = j + 1
			x.adj[j] = append(x.adj[j], i)
		}
	}
}

func (x *conflictIndex) collect(j int, e *event) {
	for _, k := range e.writes {
		x.add(j, x.writers[k])
		x.add(j, x.readers[k])
		if k == "*" {
			x.addAllBefore(j)
		}
		if isMacroKey(k) {
			x.add(j, x.dynamic)
		}
	}
	for _, k := range e.reads {
		x.add(j, x.writers[k])
		if k == "*" {
			x.add(j, x.defWriters)
		}
	}
	x.add(j, x.barrier)
}

func (x *conflictIndex) addAllBefore(j int) {
	for i := 0; i < j; i++ {
		x.add(j, []int{i})
	}
}

func (x *conflictIndex) record(j int, e *event) {
	for _, k := range e.writes {
		x.writers[k] = append(x.writers[k], j)
		if k == "*" {
			x.barrier = append(x.barrier, j)
		}
		if isMacroKey(k) {
			x.defWriters = append(x.defWriters, j)
		}
	}
	for _, k := range e.reads {
		x.readers[k] = append(x.readers[k], j)
		if k == "*" {
			x.dynamic = append(x.dynamic, j)
		}
	}
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
