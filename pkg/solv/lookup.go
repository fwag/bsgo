package solv

// PackageByName returns the visible package whose name is exactly name, from
// the highest priority repository that has one. It is used to look up
// packages the configuration names directly (Preinstall, VMinstall) without
// expanding their dependencies.
func (p *Pool) PackageByName(name string) (ResolvedPackage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.prepareLocked(); err != nil {
		return ResolvedPackage{}, false
	}
	s, ok := newExpander(p, newParams(nil)).pkgByName(name)
	if !ok {
		return ResolvedPackage{}, false
	}
	info := p.solvInfo(s)
	return ResolvedPackage{Name: info.name, EVR: info.evr, Arch: info.arch, SHA256: info.checksum, Location: info.location, Repo: info.repo}, true
}
