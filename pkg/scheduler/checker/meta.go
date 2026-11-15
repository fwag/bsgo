package checker

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// genMetaAlgo returns the BuildFlags genmetaalgo value; 1 is the default like
// in OBS. Algorithm 0 drops the dependencies on the other members of a cycle
// from the meta, algorithm 1 replaces them with the source commits of those
// members.
func (ps *pass) genMetaAlgo() int {
	if v, ok := ps.sched.BuildFlagsH["genmetaalgo"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 1 {
			return n
		}
	}
	return 1
}

// meta computes the identity of a build of u: sha256 over the source commit
// and the sorted sha256 of the dependency binaries (BuildJob.pm metacheck with
// binary ids instead of header md5s). The rebuild policy of the target
// narrows it: "local" looks at the source only, "direct" at the packages the
// recipe names. Binaries produced by the other members of u's cycle never
// enter the meta, so a cycle does not retrigger itself (genmetaalgo).
func (ps *pass) meta(u *unit) string {
	h := sha256.New()
	h.Write([]byte("bsgo-meta-v1\n" + u.commit + "\n"))
	policy := ps.target.Flags.EffectiveRebuild()
	if policy == "local" {
		return hex.EncodeToString(h.Sum(nil))
	}
	var ids []string
	for _, p := range u.edeps.Packages {
		if ps.inCycle(u, p.Name) || (policy == "direct" && !u.deps[p.Name]) {
			continue
		}
		ids = append(ids, p.SHA256)
	}
	sort.Strings(ids)
	for _, id := range ids {
		h.Write([]byte(id + "\n"))
	}
	if ps.genMetaAlgo() == 1 && u.group != nil && u.group.cyclic {
		for _, m := range sortedMembers(u.group) {
			if m != u {
				h.Write([]byte("cycle " + m.name + " " + m.commit + "\n"))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedMembers(g *group) []*unit {
	out := append([]*unit(nil), g.members...)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// inCycle reports whether the binary name is produced by another member of
// u's cycle.
func (ps *pass) inCycle(u *unit, name string) bool {
	if u.group == nil || !u.group.cyclic {
		return false
	}
	p := ps.prod[name]
	return p != nil && p != u && u.group.has(p)
}

// attemptID identifies one build attempt of a meta. A forced rebuild of an
// unchanged meta is a new attempt, distinguished by the time of the request.
func attemptID(meta string, forcedAt time.Time) string {
	if forcedAt.IsZero() {
		return meta
	}
	sum := sha256.Sum256([]byte(meta + "|force|" + strconv.FormatInt(forcedAt.UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}

var jobNamespace = uuid.MustParse("6f1a3c2e-5b7d-4c0a-9e41-2d8f7a1b9c53")

// jobID derives the deterministic job ID of an attempt.
func (ps *pass) jobID(u *unit, attempt string) string {
	return uuid.NewSHA1(jobNamespace, []byte(ps.prpa.String()+"\x00"+u.name+"\x00"+attempt)).String()
}

// release substitutes the counters into the Release directive.
func (ps *pass) release(ci, b int) string {
	tmpl := ps.sched.Release
	if tmpl == "" {
		tmpl = "<CI_CNT>.<B_CNT>"
	}
	if start, err := strconv.Atoi(strings.TrimSpace(ps.sched.CICntStart)); err == nil && start > 1 {
		ci += start - 1
	}
	return strings.NewReplacer("<CI_CNT>", strconv.Itoa(ci), "<B_CNT>", strconv.Itoa(b)).Replace(tmpl)
}
