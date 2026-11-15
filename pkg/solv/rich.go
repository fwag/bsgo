package solv

import "sort"

// Rich dependency operators, numbered like Build::Rpm::parse_rich_dep.
const (
	cLeaf    = 0
	cAnd     = 1
	cOr      = 2
	cIf      = 3
	cUnless  = 4
	cWith    = 6
	cWithout = 7
)

// Tri-state result of normalizing a rich dependency.
const (
	nFalse = 0
	nTrue  = 1
	nList  = -1
)

// cplx is a parsed rich dependency tree. For cIf and cUnless with an else
// branch, c is the else part (b is the condition).
type cplx struct {
	op      int
	a, b, c *cplx
	dep     depID
}

// clause is a conjunction of literals: positive values are solvable ids,
// negative values are negated solvable ids.
type clause []solvID

// normDep is a normalized rich dependency clause.
type normDep struct {
	str     string
	deptype int // 0 requires, 1 conflicts, 2 recommends
	cond    []solvID
	q       []solvID
	handled bool
}

// buildCplx converts a libsolv dependency into the OBS rich dependency tree.
func (p *Pool) buildCplx(d depID) *cplx {
	name, evr, flags, ok := p.decompose(d)
	if !ok || !p.isComplex(d) {
		return &cplx{op: cLeaf, dep: d}
	}
	op := map[int]int{relAnd: cAnd, relOr: cOr, relCond: cIf, relUnless: cUnless, relWith: cWith, relWithout: cWithout}[flags]
	if op == cLeaf {
		return &cplx{op: cLeaf, dep: d}
	}
	r := &cplx{op: op, a: p.buildCplx(name)}
	if (op == cIf || op == cUnless) && evr != 0 {
		if en, ee, ef, eok := p.decompose(evr); eok && ef == relElse {
			r.b = p.buildCplx(en)
			r.c = p.buildCplx(ee)
			return r
		}
	}
	r.b = p.buildCplx(evr)
	return r
}

type normalizer struct {
	e       *expander
	pname   string
	hasP    bool
	ignore  map[string]bool
	xignore map[string]bool
}

func (n *normalizer) ignored(ri string) bool {
	if n.ignore[ri] || n.xignore[ri] {
		return true
	}
	if n.hasP {
		k := n.pname + ":" + ri
		return n.ignore[k] || n.xignore[k]
	}
	return false
}

func uniqSorted(ids []solvID) []solvID {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := ids[:0]
	for i, v := range ids {
		if i == 0 || v != ids[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func cplxMix(q1, q2 []clause, todnf bool) (int, []clause) {
	var q []clause
	for _, c1 := range q1 {
		for _, c2 := range q2 {
			m := make([]solvID, 0, len(c1)+len(c2))
			m = append(m, c1...)
			m = append(m, c2...)
			m = uniqSorted(m)
			if !hasContradiction(m) {
				q = append(q, clause(m))
			}
		}
	}
	if len(q) == 0 {
		if todnf {
			return nFalse, nil
		}
		return nTrue, nil
	}
	return nList, q
}

func hasContradiction(c []solvID) bool {
	seen := make(map[solvID]bool, len(c))
	for _, v := range c {
		seen[v] = true
	}
	for _, v := range c {
		if seen[-v] {
			return true
		}
	}
	return false
}

func cplxInv(f int, q []clause) (int, []clause) {
	if f == nFalse || f == nTrue {
		return 1 - f, nil
	}
	out := make([]clause, len(q))
	for i, c := range q {
		nc := make(clause, len(c))
		for j, v := range c {
			nc[j] = -v
		}
		out[i] = nc
	}
	return nList, out
}

func (n *normalizer) leaf(r *cplx, todnf bool) (int, []clause) {
	ri := n.e.pool.baseName(r.dep)
	if !todnf && n.ignored(ri) {
		return nTrue, nil
	}
	q := n.e.provs(r.dep)
	if len(q) == 0 {
		return nFalse, nil
	}
	if todnf {
		out := make([]clause, len(q))
		for i, v := range q {
			out[i] = clause{v}
		}
		return nList, out
	}
	return nList, []clause{append(clause(nil), q...)}
}

// rec ports normalize_cplx_rec of Build::Expand.
func (n *normalizer) rec(r *cplx, todnf bool) (int, []clause) {
	switch {
	case r.op == cLeaf:
		return n.leaf(r, todnf)
	case r.op == cIf && r.c != nil:
		return n.ifElse(r, todnf)
	case r.op == cUnless && r.c != nil:
		return n.unlessElse(r, todnf)
	case r.op == cAnd || r.op == cUnless:
		return n.andUnless(r, todnf)
	case r.op == cOr || r.op == cIf:
		return n.orIf(r, todnf)
	case r.op == cWith || r.op == cWithout:
		return n.withWithout(r, todnf)
	}
	return nFalse, nil
}

func (n *normalizer) ifElse(r *cplx, todnf bool) (int, []clause) {
	n1, q1 := n.rec(&cplx{op: cIf, a: r.a, b: r.b}, todnf)
	n2, q2 := n.rec(&cplx{op: cOr, a: r.b, b: r.c}, todnf)
	switch {
	case n1 == nFalse || n2 == nFalse:
		return nFalse, nil
	case n1 == nTrue:
		return n2, q2
	case n2 == nTrue:
		return n1, q1
	case !todnf:
		return nList, append(q1, q2...)
	}
	return cplxMix(q1, q2, todnf)
}

func (n *normalizer) unlessElse(r *cplx, todnf bool) (int, []clause) {
	n1, q1 := n.rec(&cplx{op: cUnless, a: r.a, b: r.b}, todnf)
	n2, q2 := n.rec(&cplx{op: cAnd, a: r.b, b: r.c}, todnf)
	switch {
	case n1 == nTrue || n2 == nTrue:
		return nTrue, nil
	case n1 == nFalse:
		return n2, q2
	case n2 == nFalse:
		return n1, q1
	case todnf:
		return nList, append(q1, q2...)
	}
	return cplxMix(q1, q2, todnf)
}

func (n *normalizer) andUnless(r *cplx, todnf bool) (int, []clause) {
	todnf2 := todnf
	if r.op == cUnless {
		todnf2 = !todnf
	}
	n1, q1 := n.rec(r.a, todnf)
	n2, q2 := n.rec(r.b, todnf2)
	if r.op == cUnless {
		n2, q2 = cplxInv(n2, q2)
	}
	switch {
	case n1 == nFalse || n2 == nFalse:
		return nFalse, nil
	case n1 == nTrue:
		return n2, q2
	case n2 == nTrue:
		return n1, q1
	case !todnf:
		return nList, append(q1, q2...)
	}
	return cplxMix(q1, q2, todnf)
}

func (n *normalizer) orIf(r *cplx, todnf bool) (int, []clause) {
	todnf2 := todnf
	if r.op == cIf {
		todnf2 = !todnf
	}
	n1, q1 := n.rec(r.a, todnf)
	n2, q2 := n.rec(r.b, todnf2)
	if r.op == cIf {
		n2, q2 = cplxInv(n2, q2)
	}
	switch {
	case n1 == nTrue || n2 == nTrue:
		return nTrue, nil
	case n1 == nFalse:
		return n2, q2
	case n2 == nFalse:
		return n1, q1
	case todnf:
		return nList, append(q1, q2...)
	}
	return cplxMix(q1, q2, todnf)
}

func positiveOnly(c clause) bool {
	for _, v := range c {
		if v < 0 {
			return false
		}
	}
	return true
}

func (n *normalizer) withWithout(r *cplx, todnf bool) (int, []clause) {
	n1, q1 := n.rec(r.a, false)
	n2, q2 := n.rec(r.b, false)
	if n2 == nFalse && r.op == cWithout {
		q2 = []clause{{}}
		n2 = nList
	}
	if n1 != nList || n2 != nList || len(q1) != 1 || len(q2) != 1 {
		return nFalse, nil
	}
	if !positiveOnly(q1[0]) || !positiveOnly(q2[0]) {
		return nFalse, nil
	}
	in2 := make(map[solvID]bool, len(q2[0]))
	for _, v := range q2[0] {
		in2[v] = true
	}
	var q []solvID
	for _, v := range q1[0] {
		if in2[v] == (r.op == cWith) {
			q = append(q, v)
		}
	}
	if len(q) == 0 {
		return nFalse, nil
	}
	if todnf {
		out := make([]clause, len(q))
		for i, v := range q {
			out[i] = clause{v}
		}
		return nList, out
	}
	return nList, []clause{q}
}

// normalizeRich ports Build::Expand::normalizerich. p is 0 for direct deps.
func (e *expander) normalizeRich(p solvID, dep depID, deptype int, ignore, xignore map[string]bool) []*normDep {
	n := &normalizer{e: e, ignore: ignore, xignore: xignore}
	if p != 0 {
		n.hasP = true
		n.pname = e.pool.name(p)
	}
	str := e.pool.depString(dep)
	tree := e.pool.buildCplx(dep)
	if deptype == 1 {
		return n.toNormDeps(str, deptype, p, n.conflictClauses(tree, p, str))
	}
	f, q := n.rec(tree, false)
	if f == nTrue {
		return nil
	}
	if f == nFalse {
		if deptype != 2 {
			e.errNothingProvides(p, str)
		}
		return nil
	}
	return n.toNormDeps(str, deptype, p, q)
}

// conflictClauses normalizes a rich conflict into its negated clauses.
func (n *normalizer) conflictClauses(tree *cplx, p solvID, str string) []clause {
	f, q := n.rec(tree, true)
	f, q = cplxInv(f, q)
	if f != nFalse {
		return q
	}
	if p != 0 {
		n.e.errs = append(n.e.errs, n.e.pool.name(p)+" conflicts with always true "+str)
	} else {
		n.e.errs = append(n.e.errs, "conflict with always true "+str)
	}
	return q
}

func (n *normalizer) toNormDeps(str string, deptype int, p solvID, q []clause) []*normDep {
	out := make([]*normDep, 0, len(q))
	for _, c := range q {
		nd := &normDep{str: str, deptype: deptype}
		for _, v := range c {
			switch {
			case v > 0:
				nd.q = append(nd.q, v)
			case -v != p:
				nd.cond = append(nd.cond, -v)
			}
		}
		out = append(out, nd)
	}
	return out
}
