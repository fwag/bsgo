package obsimport

import (
	"regexp"
	"strings"
)

type guardKind int

const (
	guardProtected guardKind = iota
	guardPrivate
	guardRepo
	guardArch
)

// guard is a conditional that maps onto a structured scope.
type guard struct {
	kind   guardKind
	repo   string
	arches []string
	cond   *condNode
}

var (
	scopeGuardRe = regexp.MustCompile(`^\s*%if\s+0%\{\?(_is_in_project|_is_this_project)\}\s*$`)
	repoGuardRe  = regexp.MustCompile(`^\s*%if\s+"%\{?_repository\}?"\s*==\s*"([^"%]+)"\s*$`)
	archGuardRe  = regexp.MustCompile(`^\s*%ifarch\s+([A-Za-z0-9_]+(?:\s+[A-Za-z0-9_]+)*)\s*$`)
)

// guardOf recognizes the conditionals of ADR 0010. It returns nil and the
// reason when the block must stay verbatim. A block with %else or %elif
// branches never maps: its branches are not independent scopes.
func (c *classifier) guardOf(cn *condNode) (*guard, string) {
	if cn.other {
		return nil, "conditional with %else or %elif branch"
	}
	head := cn.head.Text
	if m := scopeGuardRe.FindStringSubmatch(head); m != nil {
		if m[1] == "_is_in_project" {
			return &guard{kind: guardProtected, cond: cn}, ""
		}
		return &guard{kind: guardPrivate, cond: cn}, ""
	}
	if m := repoGuardRe.FindStringSubmatch(head); m != nil {
		if !c.meta.hasRepo(m[1]) {
			return nil, "repository " + m[1] + " is not in _meta"
		}
		return &guard{kind: guardRepo, repo: m[1], cond: cn}, ""
	}
	if m := archGuardRe.FindStringSubmatch(head); m != nil {
		return &guard{kind: guardArch, arches: strings.Fields(m[1]), cond: cn}, ""
	}
	return nil, "conditional has no structured scope: " + strings.TrimSpace(head)
}

// destsFor maps a chain of guards, outermost first, to destinations. The
// second result is false when no structured scope represents the chain.
func destsFor(gs []guard) ([]dest, bool) {
	switch len(gs) {
	case 0:
		return []dest{{scope: scopePublic}}, true
	case 1:
		return singleGuardDests(gs[0]), true
	case 2:
		if gs[0].kind == guardRepo && gs[1].kind == guardArch {
			return archDests(gs[1].arches, scopeTargetArch, gs[0].repo), true
		}
	}
	return nil, false
}

func singleGuardDests(g guard) []dest {
	switch g.kind {
	case guardProtected:
		return []dest{{scope: scopeProtected}}
	case guardPrivate:
		return []dest{{scope: scopePrivate}}
	case guardRepo:
		return []dest{{scope: scopeTarget, repo: g.repo}}
	default:
		return archDests(g.arches, scopeArch, "")
	}
}

func archDests(arches []string, scope scopeKind, repo string) []dest {
	out := make([]dest, 0, len(arches))
	for _, a := range arches {
		out = append(out, dest{scope: scope, repo: repo, arch: a})
	}
	return out
}
