package checker

import "sort"

// group is a strongly connected component of the in-prpa dependency graph.
// Units of a cyclic group depend on each other and are built together.
type group struct {
	members []*unit
	cyclic  bool
	// ndepend is the number of distinct units that depend on at least one
	// member. The cycle is ready only as a whole, so all of them wait for
	// every member; it includes the members themselves. Zero for plain units.
	ndepend int
}

func (g *group) has(u *unit) bool {
	for _, m := range g.members {
		if m == u {
			return true
		}
	}
	return false
}

// buildGraph computes the in-prpa dependency graph (unit A depends on unit B
// when a build dependency of A is produced by B), its strongly connected
// components and the build order: providers before consumers, with the units
// of a cycle ordered like OBS cycsort.
func (ps *pass) buildGraph() {
	ps.buildProducers()
	var nodes []*unit
	for _, u := range ps.units {
		if u.fixed == nil {
			nodes = append(nodes, u)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].name < nodes[j].name })
	deps := make(map[*unit][]*unit, len(nodes))
	dependents := make(map[*unit][]*unit, len(nodes))
	for _, u := range nodes {
		deps[u] = ps.unitDeps(u)
		for _, d := range deps[u] {
			d.ndepend++
			dependents[d] = append(dependents[d], u)
		}
	}
	for _, scc := range tarjan(nodes, deps) {
		g := &group{members: scc, cyclic: len(scc) > 1}
		if g.cyclic {
			g.members = cycsort(scc, deps)
			g.ndepend = cycleDependents(g.members, dependents)
		}
		for _, m := range g.members {
			m.group = g
		}
		ps.groups = append(ps.groups, g)
	}
}

// cycleDependents counts the distinct units depending on any member.
func cycleDependents(members []*unit, dependents map[*unit][]*unit) int {
	seen := map[*unit]bool{}
	for _, m := range members {
		for _, d := range dependents[m] {
			seen[d] = true
		}
	}
	return len(seen)
}

// unitDeps returns the active units u depends on, sorted by name, without u.
func (ps *pass) unitDeps(u *unit) []*unit {
	seen := map[*unit]bool{}
	var out []*unit
	for _, n := range u.depNames() {
		p := ps.prod[n]
		if p == nil || p == u || p.fixed != nil || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// tarjan returns the strongly connected components of the graph in
// dependency order: a component comes after every component it depends on.
// Nodes and edges are visited in sorted order, so the result is
// deterministic.
func tarjan(nodes []*unit, deps map[*unit][]*unit) [][]*unit {
	t := &tarjanState{
		deps:  deps,
		index: map[*unit]int{}, low: map[*unit]int{}, on: map[*unit]bool{},
	}
	for _, n := range nodes {
		if _, seen := t.index[n]; !seen {
			t.visit(n)
		}
	}
	return t.out
}

type tarjanState struct {
	deps       map[*unit][]*unit
	index, low map[*unit]int
	on         map[*unit]bool
	stack      []*unit
	next       int
	out        [][]*unit
}

func (t *tarjanState) visit(v *unit) {
	t.index[v], t.low[v] = t.next, t.next
	t.next++
	t.stack = append(t.stack, v)
	t.on[v] = true
	for _, w := range t.deps[v] {
		if _, seen := t.index[w]; !seen {
			t.visit(w)
			t.low[v] = min(t.low[v], t.low[w])
		} else if t.on[w] {
			t.low[v] = min(t.low[v], t.index[w])
		}
	}
	if t.low[v] != t.index[v] {
		return
	}
	var scc []*unit
	for {
		w := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.on[w] = false
		scc = append(scc, w)
		if w == v {
			break
		}
	}
	sort.Slice(scc, func(i, j int) bool { return scc[i].name < scc[j].name })
	t.out = append(t.out, scc)
}

// cycsort orders the members of a cycle like BSSched::Checker::cycsort: edges
// that exist in both directions are dropped, the rest is sorted providers
// first, and ties are broken by name.
func cycsort(members []*unit, deps map[*unit][]*unit) []*unit {
	in := map[*unit]bool{}
	for _, m := range members {
		in[m] = true
	}
	dependsOn := func(a, b *unit) bool {
		for _, d := range deps[a] {
			if d == b {
				return true
			}
		}
		return false
	}
	cdeps := map[*unit][]*unit{}
	for _, a := range members {
		for _, b := range deps[a] {
			if in[b] && !dependsOn(b, a) {
				cdeps[a] = append(cdeps[a], b)
			}
		}
	}
	var out []*unit
	seen := map[*unit]bool{}
	var visit func(*unit)
	visit = func(u *unit) {
		if seen[u] {
			return
		}
		seen[u] = true
		for _, d := range cdeps[u] {
			visit(d)
		}
		out = append(out, u)
	}
	for _, m := range members {
		visit(m)
	}
	return out
}
