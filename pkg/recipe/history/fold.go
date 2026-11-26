package history

import "maps"

// Unknown is the version of a commit without a usable main recipe, as in OBS.
const Unknown = "unknown"

// Point is the memo of one commit: the main recipe found there, its version
// and release start value, and the version table after folding the
// first-parent history up to and including the commit.
type Point struct {
	Commit     string
	MainRecipe string // file name, "" when none
	Version    string // Unknown when no recipe or no usable version
	// ReleaseStart is the leading number of the recipe Release (OBS
	// getcommitinfo), 0 when there is none.
	ReleaseStart int
	// VT maps a version to its counter after this commit.
	VT map[string]int
}

// step is one first-parent commit with what its main recipe says.
type step struct {
	Commit       string
	MainRecipe   string
	Version      string
	ReleaseStart int
}

// fold applies the OBS vrev rule to steps, oldest first, on top of base (nil
// for the root of the history). It returns the Point of the last step; base
// is not modified.
func fold(base *Point, steps []step) Point {
	vt := map[string]int{}
	prev, hasPrev := "", false
	if base != nil {
		maps.Copy(vt, base.VT)
		prev, hasPrev = base.Version, true
	}
	var last Point
	for _, s := range steps {
		n := max(vt[s.Version]+1, s.ReleaseStart)
		if hasPrev && s.Version != prev && (s.Version == Unknown || prev == Unknown) {
			n = max(n, vt[prev]+1)
		}
		vt[s.Version] = n
		prev, hasPrev = s.Version, true
		last = Point{Commit: s.Commit, MainRecipe: s.MainRecipe, Version: s.Version, ReleaseStart: s.ReleaseStart}
	}
	last.VT = vt
	return last
}
