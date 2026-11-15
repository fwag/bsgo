package solv

import (
	"container/heap"
	"strings"
)

type nameHeap struct {
	ids  []solvID
	name func(solvID) string
}

func (h nameHeap) Len() int           { return len(h.ids) }
func (h nameHeap) Less(i, j int) bool { return h.name(h.ids[i]) < h.name(h.ids[j]) }
func (h nameHeap) Swap(i, j int)      { h.ids[i], h.ids[j] = h.ids[j], h.ids[i] }
func (h *nameHeap) Push(x any)        { h.ids = append(h.ids, x.(solvID)) }
func (h *nameHeap) Pop() any {
	n := len(h.ids)
	v := h.ids[n-1]
	h.ids = h.ids[:n-1]
	return v
}

// requireEdges maps each installed package to the installed packages it
// requires (excluding itself).
func (e *expander) requireEdges() map[solvID][]solvID {
	edges := make(map[solvID][]solvID, len(e.order))
	for _, s := range e.order {
		seen := map[solvID]bool{s: true}
		for _, r := range e.pool.solvableDeps(s, depRequires) {
			if e.pool.isComplex(r) || strings.HasPrefix(e.pool.depString(r), "rpmlib(") {
				continue
			}
			for _, q := range e.provs(r) {
				if e.installed[q] && !seen[q] {
					seen[q] = true
					edges[s] = append(edges[s], q)
				}
			}
		}
	}
	return edges
}

// sortInstalled orders the installed set so providers precede their
// requirers. Ready packages are taken by name; when only a cycle is left the
// alphabetically first remaining package is released.
func (e *expander) sortInstalled() []solvID {
	edges := e.requireEdges()
	waiting := make(map[solvID]int, len(e.order))
	users := make(map[solvID][]solvID, len(e.order))
	for s, deps := range edges {
		waiting[s] = len(deps)
		for _, d := range deps {
			users[d] = append(users[d], s)
		}
	}
	ready := &nameHeap{name: e.pool.name}
	for _, s := range e.order {
		if waiting[s] == 0 {
			ready.ids = append(ready.ids, s)
		}
	}
	heap.Init(ready)
	placed := make(map[solvID]bool, len(e.order))
	out := make([]solvID, 0, len(e.order))
	for len(out) < len(e.order) {
		if ready.Len() == 0 {
			heap.Push(ready, e.firstUnplaced(placed))
		}
		s := heap.Pop(ready).(solvID)
		if placed[s] {
			continue
		}
		placed[s] = true
		out = append(out, s)
		for _, u := range users[s] {
			waiting[u]--
			if waiting[u] == 0 && !placed[u] {
				heap.Push(ready, u)
			}
		}
	}
	return out
}

func (e *expander) firstUnplaced(placed map[solvID]bool) solvID {
	var best solvID
	for _, s := range e.order {
		if placed[s] {
			continue
		}
		if best == 0 || e.pool.name(s) < e.pool.name(best) {
			best = s
		}
	}
	return best
}
