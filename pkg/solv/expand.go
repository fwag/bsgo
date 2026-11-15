package solv

import (
	"sort"
	"strings"
)

// expandParams is the prepared form of an ExpandConfig (Build::init_helper_hashes
// and the expandflags:* lookups of Build::read_config).
type expandParams struct {
	prefer          map[string]bool
	preferList      []string
	ignore          map[string]bool
	conflictAll     []string
	conflict        map[string][]string
	fileProvides    map[string][]string
	keepFileReq     bool
	ignoreConflicts bool
	doRecommends    bool
}

type todoItem struct {
	str  string
	dep  depID
	norm *normDep
	from solvID
}

type condRef struct {
	c *normDep
	p solvID
}

// expander is one run of Build::Expand::expand. The pool must be locked and
// prepared for the lifetime of the run.
type expander struct {
	pool    *Pool
	par     *expandParams
	ignore  map[string]bool
	xignore map[string]bool

	installed      map[solvID]bool
	order          []solvID
	why            map[solvID]string
	todo           []todoItem
	todoInst       []solvID
	todoRecommends []solvID
	todoCond       map[solvID][]condRef
	recommended    map[solvID]bool
	recTodo        []solvID
	aconflicts     map[solvID][]string
	errs           []string
	byName         map[string]solvID
}

func newExpander(p *Pool, par *expandParams) *expander {
	pc := *par
	return &expander{
		pool:        p,
		par:         &pc,
		ignore:      par.ignore,
		installed:   map[solvID]bool{},
		why:         map[solvID]string{},
		todoCond:    map[solvID][]condRef{},
		recommended: map[solvID]bool{},
		aconflicts:  map[solvID][]string{},
		byName:      map[string]solvID{},
	}
}

func (e *expander) errNothingProvides(p solvID, r string) {
	if p != 0 {
		e.errs = append(e.errs, "nothing provides "+r+" needed by "+e.pool.name(p))
		return
	}
	e.errs = append(e.errs, "nothing provides "+r)
}

// provs returns the visible providers of dep, applying the file dependency
// policy (FileProvides, KeepFileRequires) of OBS.
func (e *expander) provs(dep depID) []solvID {
	if dep == 0 {
		return nil
	}
	if _, _, _, rel := e.pool.decompose(dep); rel {
		return e.pool.rawProviders(dep)
	}
	name := e.pool.baseName(dep)
	if !strings.HasPrefix(name, "/") {
		return e.pool.rawProviders(dep)
	}
	q := e.explicitProviders(dep)
	var extra []solvID
	for _, pn := range e.par.fileProvides[name] {
		if s, ok := e.pkgByName(pn); ok {
			extra = append(extra, s)
		}
	}
	if len(extra) == 0 {
		return q
	}
	return unifyIDs(append(append([]solvID(nil), q...), extra...))
}

// explicitProviders keeps providers that list the plain dependency among
// their provides. File entries of primary.xml are not provides in OBS (the
// reference createrepomddeps ignores them); FileProvides maps them explicitly.
func (e *expander) explicitProviders(dep depID) []solvID {
	var out []solvID
	for _, s := range e.pool.rawProviders(dep) {
		for _, d := range e.pool.solvableDeps(s, depProvides) {
			if d == dep {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func unifyIDs(ids []solvID) []solvID {
	seen := make(map[solvID]bool, len(ids))
	out := ids[:0:0]
	for _, v := range ids {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (e *expander) provsStr(r string) []solvID {
	return e.provs(e.pool.str2dep(r))
}

// pkgByName finds the visible solvable whose package name is name.
func (e *expander) pkgByName(name string) (solvID, bool) {
	if s, ok := e.byName[name]; ok {
		return s, s != 0
	}
	var found solvID
	for _, s := range e.pool.rawProviders(e.pool.str2dep(name)) {
		if e.pool.name(s) == name {
			found = s
			break
		}
	}
	e.byName[name] = found
	return found, found != 0
}

func (e *expander) names(ids []solvID) []string {
	out := make([]string, len(ids))
	for i, s := range ids {
		out[i] = e.pool.name(s)
	}
	return out
}

func (e *expander) addInst(s solvID, why string) {
	if _, ok := e.why[s]; !ok {
		e.why[s] = why
	}
	e.todoInst = append(e.todoInst, s)
}

func (e *expander) isRichStr(r string) bool {
	return strings.HasPrefix(r, "(") && strings.HasSuffix(r, ")")
}

// run ports Build::Expand::expand. It returns false when errors were recorded.
func (e *expander) run(args []string) bool {
	p, dds := e.parseArgs(args)
	e.configConflicts()
	p = e.directConflicts(p)
	p = e.directDeps(p)
	for _, r := range append(append([]string(nil), p...), dds...) {
		e.queueRequest(r)
	}
	e.checkDirectInst()
	if len(e.errs) > 0 {
		return false
	}
	return e.mainLoop()
}

func (e *expander) parseArgs(args []string) (p, dds []string) {
	e.xignore = map[string]bool{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			e.xignore[a[1:]] = true
		}
	}
	for flag, set := range map[string]*bool{
		"-ignoreconflicts--":  &e.par.ignoreConflicts,
		"-keepfilerequires--": &e.par.keepFileReq,
		"-dorecommends--":     &e.par.doRecommends,
	} {
		if e.xignore[flag] {
			*set = true
		}
	}
	if e.xignore["-ignoreignore--"] {
		e.ignore = map[string]bool{}
	}
	if !e.xignore["-directdepsend--"] {
		return args, nil
	}
	delete(e.xignore, "-directdepsend--")
	for i, a := range args {
		if a == "--directdepsend--" {
			for _, d := range args[i+1:] {
				if !strings.HasPrefix(d, "-") {
					dds = append(dds, d)
				}
			}
			return args[:i], dds
		}
	}
	return args, nil
}

func (e *expander) configConflicts() {
	for _, n := range e.par.conflictAll {
		if s, ok := e.pkgByName(n); ok {
			e.aconflicts[s] = append(e.aconflicts[s], "is in conflict")
		}
	}
}

func (e *expander) directConflicts(args []string) []string {
	var rest []string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "!"):
			e.directConflict(a)
		case strings.HasPrefix(a, "-"):
		default:
			rest = append(rest, a)
		}
	}
	return rest
}

func (e *expander) directConflict(a string) {
	exact := strings.HasPrefix(a, "!!")
	r := strings.TrimPrefix(strings.TrimPrefix(a, "!"), "!")
	if e.isRichStr(r) {
		nds := e.normalizeRich(0, e.pool.str2dep(r), 1, nil, nil)
		nac := map[solvID]string{}
		e.checkCondInst(0, nds, e.installed, nac, true)
		e.mergeConflicts(nac)
		return
	}
	q := e.provsStr(r)
	if exact {
		q = e.nevrMatch(r, q)
	}
	for _, s := range q {
		e.aconflicts[s] = append(e.aconflicts[s], "is in conflict")
	}
}

func (e *expander) mergeConflicts(nac map[solvID]string) {
	keys := make([]solvID, 0, len(nac))
	for k := range nac {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		e.aconflicts[k] = append(e.aconflicts[k], nac[k])
	}
}

// nevrMatch ports Build::Expand::nevrmatch: keep providers whose package name
// equals the name part of dep r.
func (e *expander) nevrMatch(r string, q []solvID) []solvID {
	rn := r
	if i := strings.IndexAny(rn, " <=>"); i >= 0 {
		rn = strings.TrimRight(rn[:i], " ")
	}
	var out []solvID
	for _, s := range q {
		if e.pool.name(s) == rn {
			out = append(out, s)
		}
	}
	return out
}

func (e *expander) directDeps(args []string) []string {
	var rest []string
	for _, r := range args {
		if e.isRichStr(r) {
			rest = append(rest, r)
			continue
		}
		q := e.provsStr(r)
		if len(q) > 1 {
			q = e.filterConflicted(q)
		}
		pn := strings.SplitN(r, " ", 2)[0]
		var exact []solvID
		for _, s := range q {
			if e.pool.name(s) == pn {
				exact = append(exact, s)
			}
		}
		if len(exact) != 1 {
			rest = append(rest, r)
			continue
		}
		e.addInst(exact[0], "direct dep "+r)
	}
	return rest
}

func (e *expander) filterConflicted(q []solvID) []solvID {
	var out []solvID
	for _, s := range q {
		if len(e.aconflicts[s]) == 0 {
			out = append(out, s)
		}
	}
	return out
}

func (e *expander) queueRequest(r string) {
	if !e.isRichStr(r) {
		e.todo = append(e.todo, todoItem{str: normalizeDepString(r), dep: e.pool.str2dep(r)})
		return
	}
	nds := e.normalizeRich(0, e.pool.str2dep(r), 0, nil, nil)
	nac := map[solvID]string{}
	e.checkCondInst(0, nds, e.installed, nac, true)
	e.mergeConflicts(nac)
}

func (e *expander) checkDirectInst() {
	for _, s := range e.todoInst {
		for _, m := range e.aconflicts[s] {
			e.errs = append(e.errs, e.pool.name(s)+" "+m)
		}
	}
}

func (e *expander) mainLoop() bool {
	for len(e.todo) > 0 || len(e.todoInst) > 0 {
		if len(e.todoInst) > 0 && !e.installBatch() {
			return false
		}
		e.runPasses()
		if len(e.errs) > 0 {
			return false
		}
		e.queueRecommends()
	}
	return len(e.errs) == 0
}

func (e *expander) installBatch() bool {
	e.todoInst = unifyIDs(e.todoInst)
	e.checkDirectInst()
	if len(e.errs) > 0 {
		return false
	}
	for _, s := range e.todoInst {
		e.markInstalled(s)
	}
	if len(e.errs) > 0 {
		return false
	}
	for _, s := range e.todoInst {
		e.checkOwnDeps(s)
	}
	e.todoInst = nil
	return len(e.errs) == 0
}

func (e *expander) markInstalled(s solvID) {
	if e.par.doRecommends {
		e.todoRecommends = append(e.todoRecommends, s)
	}
	e.installed[s] = true
	e.order = append(e.order, s)
	refs := e.todoCond[s]
	delete(e.todoCond, s)
	for _, ref := range refs {
		nac := map[solvID]string{}
		e.checkCondInstTodo(ref.p, []*normDep{ref.c}, nac, false)
		e.mergeConflicts(nac)
	}
	delete(e.aconflicts, s)
}

func (e *expander) checkCondInstTodo(p solvID, n []*normDep, nac map[solvID]string, withCond bool) {
	e.checkCondInst(p, n, e.installed, nac, withCond)
}

// checkOwnDeps queues the dependencies of a newly installed package and
// records the conflicts and obsoletes it brings along.
func (e *expander) checkOwnDeps(s solvID) {
	pool := e.pool
	nac := map[solvID]string{}
	nao := map[solvID]string{}
	pname := pool.name(s)
	for _, cn := range e.par.conflict[pname] {
		if c, ok := e.pkgByName(cn); ok {
			nac[c] = "is in conflict with " + pname
		}
	}
	for _, r := range pool.solvableDeps(s, depRequires) {
		e.queueRequire(s, r, nac)
	}
	if !e.par.ignoreConflicts {
		e.pkgConflicts(s, nac)
		for _, r := range pool.solvableDeps(s, depObsoletes) {
			for _, o := range e.nevrMatch(pool.baseName(r), e.provs(r)) {
				nao[o] = "is obsoleted by " + pname
			}
		}
	}
	e.reportConflicts(s, nac, " conflicts with ")
	e.reportConflicts(s, nao, " obsoletes ")
	e.recTodo = append(e.recTodo, s)
}

func (e *expander) reportConflicts(s solvID, m map[solvID]string, verb string) {
	if len(m) == 0 {
		return
	}
	var names []string
	for k := range m {
		if k != s && e.installed[k] {
			names = append(names, e.pool.name(k))
		}
	}
	sort.Strings(names)
	for _, n := range names {
		e.errs = append(e.errs, e.pool.name(s)+verb+n)
	}
	e.mergeConflicts(m)
}

func (e *expander) queueRequire(s solvID, r depID, nac map[solvID]string) {
	if e.pool.isComplex(r) {
		nds := e.normalizeRich(s, r, 0, e.ignore, e.xignore)
		e.checkCondInst(s, nds, e.installed, nac, true)
		return
	}
	ri := e.pool.baseName(r)
	pname := e.pool.name(s)
	if e.ignore[pname+":"+ri] || e.xignore[pname+":"+ri] || e.ignore[ri] || e.xignore[ri] {
		return
	}
	if strings.HasPrefix(ri, "rpmlib(") {
		return
	}
	if !e.par.keepFileReq && strings.HasPrefix(ri, "/") && len(e.provs(r)) == 0 {
		return
	}
	e.todo = append(e.todo, todoItem{str: e.pool.depString(r), dep: r, from: s})
}

func (e *expander) pkgConflicts(s solvID, nac map[solvID]string) {
	pool := e.pool
	pname := pool.name(s)
	for _, r := range pool.solvableDeps(s, depConflicts) {
		if pool.isComplex(r) {
			nds := e.normalizeRich(s, r, 1, nil, nil)
			e.checkCondInst(s, nds, e.installed, nac, true)
			continue
		}
		for _, c := range e.conflictTargets(r) {
			nac[c] = "is in conflict with " + pname
		}
	}
}

// conflictTargets resolves a plain conflict dep, honoring otherproviders().
func (e *expander) conflictTargets(r depID) []solvID {
	str := e.pool.depString(r)
	if inner, ok := otherProviders(str); ok {
		return e.provsStr(inner)
	}
	return e.provs(r)
}

func otherProviders(s string) (string, bool) {
	if strings.HasPrefix(s, "otherproviders(") && strings.HasSuffix(s, ")") {
		return s[len("otherproviders(") : len(s)-1], true
	}
	return "", false
}

// checkCondInst ports check_conddeps_inst. withCond enables registering
// still-open conditions in todoCond.
func (e *expander) checkCondInst(p solvID, n []*normDep, installed map[solvID]bool, nac map[solvID]string, withCond bool) {
	for _, c := range n {
		if c.handled || anyInstalled(installed, c.q) {
			continue
		}
		var cx []solvID
		for _, s := range c.cond {
			if !installed[s] {
				cx = append(cx, s)
			}
		}
		if len(cx) == 0 {
			e.condFulfilled(p, c)
			continue
		}
		e.condOpen(p, c, cx, nac, withCond)
	}
}

func anyInstalled(installed map[solvID]bool, q []solvID) bool {
	for _, s := range q {
		if installed[s] {
			return true
		}
	}
	return false
}

func (e *expander) condFulfilled(p solvID, c *normDep) {
	c.handled = true
	switch {
	case len(c.q) > 0:
		e.todo = append(e.todo, todoItem{str: c.str, norm: c, from: p})
	case len(c.cond) == 0:
	case c.deptype == 0:
		e.errNothingProvides(p, c.str)
	case c.deptype == 2:
	default:
		names := e.names(c.cond)
		sort.Strings(names)
		for _, n := range names {
			if p != 0 {
				e.errs = append(e.errs, e.pool.name(p)+" conflicts with "+n)
			} else {
				e.errs = append(e.errs, "conflicts with "+n)
			}
		}
	}
}

func (e *expander) condOpen(p solvID, c *normDep, cx []solvID, nac map[solvID]string, withCond bool) {
	if len(c.q) == 0 && len(cx) == 1 {
		if c.deptype == 2 {
			return
		}
		nac[cx[0]] = e.condMessage(p, c)
		return
	}
	if withCond {
		for _, s := range cx {
			e.todoCond[s] = append(e.todoCond[s], condRef{c: c, p: p})
		}
	}
}

func (e *expander) condMessage(p solvID, c *normDep) string {
	switch {
	case c.deptype == 0 && p != 0:
		return "conflicts with " + c.str + " needed by " + e.pool.name(p)
	case c.deptype == 0:
		return "conflicts with " + c.str
	case p != 0:
		return "is in conflict with " + e.pool.name(p)
	}
	return "is in conflict"
}

func (e *expander) runPasses() {
	for pass := 0; pass <= 6; pass++ {
		if pass == 5 {
			continue // supplements pass: ExpandFlags dosupplements is not supported
		}
		var next []todoItem
		for _, it := range e.todo {
			if e.processItem(it, pass) {
				next = append(next, it)
			}
		}
		e.todo = next
		if len(e.todoInst) > 0 {
			break
		}
	}
}

func (e *expander) itemProviders(it todoItem) []solvID {
	if it.norm != nil {
		return it.norm.q
	}
	return e.provs(it.dep)
}

// processItem handles one pending requirement in the given pass. It returns
// true when the decision has to be deferred to a later pass.
func (e *expander) processItem(it todoItem, pass int) bool {
	q := e.itemProviders(it)
	if anyInstalled(e.installed, q) {
		return false
	}
	var pp, pn string
	if it.from != 0 {
		pp = e.pool.name(it.from) + ":"
		pn = " needed by " + e.pool.name(it.from)
		if e.ignoredProviders(q, pp) {
			return false
		}
	}
	if len(q) == 0 {
		e.noProvider(it, pn)
		return false
	}
	if len(q) > 1 && pass == 0 {
		return true
	}
	q, eq := e.pruneConflicts(q)
	if len(q) == 0 {
		sort.Strings(eq)
		e.errs = append(e.errs, "conflict for providers of "+it.str+pn)
		e.errs = append(e.errs, eq...)
		return false
	}
	return e.choose(it, q, pass, pp, pn)
}

func (e *expander) ignoredProviders(q []solvID, pp string) bool {
	for _, s := range q {
		n := e.pool.name(s)
		if e.ignore[n] || e.xignore[n] || e.ignore[pp+n] || e.xignore[pp+n] {
			return true
		}
	}
	return false
}

func (e *expander) noProvider(it todoItem, pn string) {
	if it.from != 0 && strings.HasPrefix(it.str, "rpmlib(") {
		return
	}
	if it.from != 0 && !e.par.keepFileReq && strings.HasPrefix(it.str, "/") {
		return
	}
	e.errs = append(e.errs, "nothing provides "+it.str+pn)
}

// pruneConflicts removes providers that conflict with the installed set and
// returns explanations for the removed ones.
func (e *expander) pruneConflicts(q []solvID) ([]solvID, []string) {
	var eq []string
	for _, s := range q {
		for _, m := range e.aconflicts[s] {
			eq = append(eq, "(provider "+e.pool.name(s)+" "+m+")")
		}
	}
	var out []solvID
	for _, s := range q {
		if len(e.aconflicts[s]) == 0 {
			out = append(out, s)
		}
	}
	if e.par.ignoreConflicts {
		return out, eq
	}
	out = e.dropWhere(out, func(s solvID) bool { return e.checkConflicts(s, &eq) })
	out = e.dropWhere(out, func(s solvID) bool { return e.checkObsoletes(s, &eq) })
	return out, eq
}

func (e *expander) dropWhere(q []solvID, bad func(solvID) bool) []solvID {
	var out []solvID
	for _, s := range q {
		if !bad(s) {
			out = append(out, s)
		}
	}
	return out
}

// checkConflicts ports checkconflicts: does candidate s conflict with an
// installed package? Messages are appended to eq.
func (e *expander) checkConflicts(s solvID, eq *[]string) bool {
	ret := false
	sname := e.pool.name(s)
	for _, r := range e.pool.solvableDeps(s, depConflicts) {
		if e.pool.isComplex(r) {
			// errors are ignored here; they are reported when s gets chosen
			saved := e.errs
			nds := e.normalizeRich(s, r, 1, nil, nil)
			e.errs = saved
			if e.condNotInst(s, nds, eq) {
				ret = true
			}
			continue
		}
		for _, c := range e.conflictTargets(r) {
			if e.installed[c] {
				*eq = append(*eq, "(provider "+sname+" conflicts with "+e.pool.name(c)+")")
				ret = true
			}
		}
	}
	return ret
}

func (e *expander) checkObsoletes(s solvID, eq *[]string) bool {
	ret := false
	sname := e.pool.name(s)
	for _, r := range e.pool.solvableDeps(s, depObsoletes) {
		for _, o := range e.nevrMatch(e.pool.baseName(r), e.provs(r)) {
			if e.installed[o] {
				*eq = append(*eq, "(provider "+sname+" obsoletes "+e.pool.name(o)+")")
				ret = true
			}
		}
	}
	return ret
}

// condNotInst ports check_conddeps_notinst.
func (e *expander) condNotInst(p solvID, n []*normDep, eq *[]string) bool {
	ret := false
	for _, c := range n {
		if len(c.q) > 0 || len(c.cond) == 0 || !allInstalled(e.installed, c.cond) {
			continue
		}
		names := e.names(c.cond)
		sort.Strings(names)
		for _, cn := range names {
			*eq = append(*eq, "(provider "+e.pool.name(p)+" conflicts with "+cn+")")
		}
		ret = true
	}
	return ret
}

func allInstalled(installed map[solvID]bool, q []solvID) bool {
	for _, s := range q {
		if !installed[s] {
			return false
		}
	}
	return true
}

func (e *expander) choose(it todoItem, q []solvID, pass int, pp, pn string) bool {
	if len(q) == 1 {
		e.addInst(q[0], "because of "+pp+it.str)
		return false
	}
	if pass < 2 {
		return true
	}
	q = e.prunePrefer(q, pp)
	if len(q) == 1 {
		e.addInst(q[0], "because of "+pp+it.str+" (prefer)")
		return false
	}
	if pass < 3 {
		return true
	}
	q = e.pickPreferred(q, pp)
	if len(q) == 1 {
		e.addInst(q[0], "because of "+pp+it.str+" (prefer list)")
		return false
	}
	if pass < 4 {
		return true
	}
	q = e.pruneRecommended(q)
	if len(q) == 1 {
		e.addInst(q[0], "because of "+pp+it.str+" (recommended)")
		return false
	}
	if pass < 6 {
		return true
	}
	names := e.names(q)
	sort.Strings(names)
	e.errs = append(e.errs, "have choice for "+it.str+pn+": "+strings.Join(names, " "))
	return false
}

func (e *expander) isPreferred(n, pp string) bool {
	return e.par.prefer[n] || e.par.prefer[pp+n]
}

// prunePrefer is pass 2: drop negatively preferred providers, then select a
// sole positively preferred one.
func (e *expander) prunePrefer(q []solvID, pp string) []solvID {
	var pq []solvID
	for _, s := range q {
		n := e.pool.name(s)
		if !e.par.prefer["-"+n] && !e.par.prefer["-"+pp+n] {
			pq = append(pq, s)
		}
	}
	if len(pq) > 0 {
		q = pq
	}
	pq = nil
	for _, s := range q {
		if e.isPreferred(e.pool.name(s), pp) {
			pq = append(pq, s)
		}
	}
	if len(pq) == 1 {
		return pq
	}
	return q
}

// pickPreferred is pass 3: among several preferred providers the one listed
// first in the Prefer list wins.
func (e *expander) pickPreferred(q []solvID, pp string) []solvID {
	var pq []solvID
	for _, s := range q {
		if e.isPreferred(e.pool.name(s), pp) {
			pq = append(pq, s)
		}
	}
	if len(pq) == 1 {
		return pq
	}
	if len(pq) == 0 {
		return q
	}
	for _, entry := range e.par.preferList {
		for _, s := range pq {
			n := e.pool.name(s)
			if entry == n || entry == pp+n {
				return []solvID{s}
			}
		}
	}
	return pq
}

// pruneRecommended is pass 4: prefer providers recommended by installed packages.
func (e *expander) pruneRecommended(q []solvID) []solvID {
	for _, s := range e.recTodo {
		for _, r := range e.pool.solvableDeps(s, depRecommends) {
			for _, t := range e.provs(r) {
				e.recommended[t] = true
			}
		}
	}
	e.recTodo = nil
	var pq []solvID
	for _, s := range q {
		if e.recommended[s] {
			pq = append(pq, s)
		}
	}
	if len(pq) > 0 {
		return pq
	}
	return q
}

// queueRecommends adds the recommends of installed packages once everything
// else is settled (ExpandFlags dorecommends).
func (e *expander) queueRecommends() {
	if len(e.todoRecommends) == 0 || len(e.todo) > 0 || len(e.todoInst) > 0 {
		return
	}
	for _, s := range e.todoRecommends {
		for _, r := range e.pool.solvableDeps(s, depRecommends) {
			if e.pool.isComplex(r) {
				nds := e.normalizeRich(s, r, 2, e.ignore, e.xignore)
				e.checkCondInst(s, nds, e.installed, nil, true)
				continue
			}
			e.queueRecommend(s, r)
		}
	}
}

func (e *expander) queueRecommend(s solvID, r depID) {
	q := e.provs(r)
	if anyInstalled(e.installed, q) {
		return
	}
	q = e.filterConflicted(q)
	if !e.par.ignoreConflicts {
		var sink []string
		q = e.dropWhere(q, func(c solvID) bool { return e.checkConflicts(c, &sink) })
		q = e.dropWhere(q, func(c solvID) bool { return e.checkObsoletes(c, &sink) })
	}
	if len(q) > 0 {
		e.todo = append(e.todo, todoItem{str: e.pool.depString(r), dep: r, from: s})
	}
}
