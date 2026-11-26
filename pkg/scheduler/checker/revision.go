package checker

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// revisionRetry is how soon a pass is repeated while units wait for the
// release number of their revision.
const revisionRetry = 15 * time.Second

// revisionOf returns the resolved package revision of the pin of u. The
// release number (vrev) is computed in the background from the git history
// (package scheduler/vrev); the pass only reads it. A revision that is not
// resolved yet makes the unit blocked, never broken: the unit is recorded as
// wanted so the resolver works on it before the backfill, and the pass is
// repeated shortly.
func (ps *pass) revisionOf(u *unit) (*storage.PackageRevision, *verdict) {
	rev, err := ps.c.d.Store.RevisionFor(ps.ctx, ps.prpa.Project, u.pkg, u.commit)
	switch {
	case err == nil && rev.Resolved():
		return rev, nil
	case err == nil:
		return nil, ps.waitForRevision(u, rev.LastError)
	case errors.Is(err, storage.ErrNotFound):
		ps.logMissing(ps.prpa.Project+"/"+u.pkg+"/"+u.commit, fmt.Sprintf(
			"%s: no revision of commit %s (manifest stored without a revision log)", u.pkg, short(u.commit)))
		return nil, ps.waitForRevision(u, "")
	}
	ps.fail(fmt.Errorf("read revision of %s: %w", u.name, err))
	return nil, &verdict{storage.StateBroken, err.Error()}
}

func (ps *pass) waitForRevision(u *unit, lastErr string) *verdict {
	ps.mu.Lock()
	ref := storage.RevisionRef{Project: ps.prpa.Project, Package: u.pkg, Commit: u.commit}
	if !ps.wantedSeen[ref] {
		if ps.wantedSeen == nil {
			ps.wantedSeen = map[storage.RevisionRef]bool{}
		}
		ps.wantedSeen[ref] = true
		ps.wanted = append(ps.wanted, ref)
	}
	ps.mu.Unlock()
	ps.noteRetry(revisionRetry)
	details := "waiting for release number (source history of " + short(u.commit) + ")"
	if lastErr != "" {
		details += ": " + lastErr
	}
	return &verdict{storage.StateBlocked, details}
}

// recordWanted tells the resolver which revisions units wait for. It is
// advisory: when it fails the units still wait, only the resolver's order is
// lost, so the failure is logged and the pass goes on.
func (ps *pass) recordWanted() {
	ps.mu.Lock()
	refs := ps.wanted
	ps.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	if err := ps.c.d.Store.WantRevisions(ps.ctx, refs); err != nil && ps.ctx.Err() == nil {
		ps.log(fmt.Sprintf("record wanted revisions: %v", err))
	}
}

func (ps *pass) log(msg string) {
	if ps.c.d.Logf != nil {
		ps.c.d.Logf("%s: %s", ps.prpa, msg)
	}
}

// missingLogInterval is how often the missing-revision line repeats per
// revision: a store written by an old binary would otherwise log it on every
// pass of every prpa.
const missingLogInterval = 10 * time.Minute

// logMissing logs msg unless it was logged for key within missingLogInterval.
func (ps *pass) logMissing(key, msg string) {
	c := ps.c
	now := c.d.Now()
	c.mu.Lock()
	last, seen := c.missingLogged[key]
	if !seen || now.Sub(last) >= missingLogInterval {
		if c.missingLogged == nil {
			c.missingLogged = map[string]time.Time{}
		}
		c.missingLogged[key] = now
		seen = false
	}
	c.mu.Unlock()
	if !seen {
		ps.log(msg)
	}
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// versrel is "<version>-<vrev>" for the version of the unit's own recipe, like
// the versrel OBS derives from the package revision at scheduling time. B_CNT
// counts the builds of one versrel.
func versrel(u *unit, vrev int) string {
	version := u.info.Version
	if version == "" || u.info.MultiVersion {
		version = storage.UnknownVersion
	}
	return version + "-" + strconv.Itoa(vrev)
}
