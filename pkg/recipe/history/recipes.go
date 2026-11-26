package history

import (
	"context"
	"regexp"
	"slices"
	"strconv"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/recipe"
	"github.com/openSUSE/bsgo/pkg/recipe/internal/gitrepo"
)

// candidateRe matches the top-level files that can be a main recipe; it must
// agree with the pathspecs of gitrepo.RecipeChanges.
var candidateRe = regexp.MustCompile(`^(?:.+\.spec|.+\.dsc|.+\.kiwi|Dockerfile.*|.+\.productcompose)$`)

// candidate is a recipe file of one commit.
type candidate struct{ Name, OID string }

// candidates lists the files OBS getcommitinfo would try for the lookup name,
// in its order: spec, dsc, kiwi, docker, productcompose (one per type). A
// pinned recipe present in files replaces the search.
func candidates(files map[string]string, lookup, pinned string) []candidate {
	if oid, ok := files[pinned]; ok && pinned != "" {
		return []candidate{{pinned, oid}}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	var picks []string
	for _, ext := range []string{"spec", "dsc", "kiwi"} {
		picks = append(picks, recipe.FindRecipe(lookup, "", ext, names))
	}
	picks = append(picks, "Dockerfile")
	if _, ok := files["Dockerfile."+lookup]; ok {
		picks[len(picks)-1] = "Dockerfile." + lookup
	}
	picks = append(picks, recipe.FindRecipe(lookup, "", "productcompose", names))
	var out []candidate
	for _, p := range picks {
		if oid, ok := files[p]; ok && p != "" {
			out = append(out, candidate{p, oid})
		}
	}
	return out
}

// verdict is what one recipe blob says.
type verdict struct {
	Version      string
	ReleaseStart int
	// Final is false when OBS would go on with the next recipe type (a spec
	// without a Version tag).
	Final bool
}

var (
	relBcntRe = regexp.MustCompile(`(\d+)\.<B_CNT>`)
	relTagRe  = regexp.MustCompile(`<RELEASE(\d+)>`)
	relLeadRe = regexp.MustCompile(`^(\d+)`)
)

// releaseStart ports the Release handling of bs_srcserver getcommitinfo.
func releaseStart(release string) int {
	for _, re := range []*regexp.Regexp{relBcntRe, relTagRe, relLeadRe} {
		if m := re.FindStringSubmatch(release); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// judge parses one recipe. Only spec recipes have a parser; any other recipe
// type, a recipe that does not parse, or a multiversion spec yields Unknown.
func judge(cfg *buildconfig.Config, lookup string, c candidate, content []byte) verdict {
	info, err := recipe.Parse(cfg, c.Name, content, recipe.Options{Package: lookup})
	if err != nil || info.Error != "" {
		return verdict{Version: Unknown, Final: true}
	}
	if info.Version == "" {
		return verdict{Version: Unknown}
	}
	v := verdict{Version: info.Version, ReleaseStart: releaseStart(info.Release), Final: true}
	if info.MultiVersion {
		v.Version = Unknown
	}
	return v
}

// stepFor picks the main recipe of one commit: the first candidate with a
// final verdict.
func stepFor(commit string, cands []candidate, verdicts map[candidate]verdict) step {
	for _, c := range cands {
		if v := verdicts[c]; v.Final {
			return step{Commit: commit, MainRecipe: c.Name, Version: v.Version, ReleaseStart: v.ReleaseStart}
		}
	}
	return step{Commit: commit, Version: Unknown}
}

// readVerdicts reads and judges every distinct candidate blob once, in
// chunks so the contents of a long history are never all in memory.
func readVerdicts(ctx context.Context, g *gitrepo.Repo, cfg *buildconfig.Config, lookup string, all map[candidate]bool) (map[candidate]verdict, int, error) {
	byOID := map[string][]candidate{}
	var oids []string
	for c := range all {
		if _, ok := byOID[c.OID]; !ok {
			oids = append(oids, c.OID)
		}
		byOID[c.OID] = append(byOID[c.OID], c)
	}
	slices.Sort(oids)
	fetched, err := g.FetchBlobs(ctx, oids)
	if err != nil {
		return nil, 0, err
	}
	out := make(map[candidate]verdict, len(all))
	for len(oids) > 0 {
		chunk := oids[:min(readChunk, len(oids))]
		oids = oids[len(chunk):]
		data, err := g.ReadBlobs(ctx, chunk)
		if err != nil {
			return nil, 0, err
		}
		for _, oid := range chunk {
			for _, c := range byOID[oid] {
				out[c] = judge(cfg, lookup, c, data[oid])
			}
		}
	}
	return out, fetched, nil
}
