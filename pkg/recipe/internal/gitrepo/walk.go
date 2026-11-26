package gitrepo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// recipePathspecs select the top-level files that can be a recipe.
var recipePathspecs = []string{
	":(glob)*.spec", ":(glob)*.dsc", ":(glob)*.kiwi", ":(glob)Dockerfile*", ":(glob)*.productcompose",
}

// FirstParent lists the first-parent chain of the fetched commit, newest
// first, at most max entries. more reports whether older commits exist.
func (g *Repo) FirstParent(ctx context.Context, max int) (commits []string, more bool, err error) {
	if max < 1 {
		return nil, false, fmt.Errorf("invalid first-parent limit %d", max)
	}
	out, err := g.git(ctx, "", "rev-list", "--first-parent", "--max-count="+strconv.Itoa(max+1), CommitRef)
	if err != nil {
		return nil, false, err
	}
	commits = strings.Fields(string(out))
	if len(commits) > max {
		return commits[:max], true, nil
	}
	return commits, false, nil
}

// FileChange is one recipe-candidate file touched by a first-parent step.
type FileChange struct {
	Name    string
	OID     string // new blob id; empty when the file was deleted
	Deleted bool
}

// Change lists the recipe-candidate files a first-parent step touched,
// relative to the previous step.
type Change struct {
	Commit string
	Files  []FileChange
}

// RecipeChanges returns the changes of the first-parent steps after base up to
// the fetched commit, oldest first; steps that touch no recipe candidate
// (*.spec, *.dsc, *.kiwi, Dockerfile*, *.productcompose at the top level) are
// omitted. An empty base means from the root commit.
func (g *Repo) RecipeChanges(ctx context.Context, base string) ([]Change, error) {
	args := []string{
		"log", "--first-parent", "--reverse", "--root", "--diff-merges=first-parent",
		"--format=%x00%H", "--raw", "-z", "--no-abbrev", "--no-renames", CommitRef,
	}
	if base != "" {
		if !ValidID(base) {
			return nil, fmt.Errorf("invalid commit id %q", base)
		}
		args = append(args, "^"+base)
	}
	args = append(args, "--")
	args = append(args, recipePathspecs...)
	out, err := g.git(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	return parseChanges(strings.Split(string(out), "\x00"))
}

// parseChanges parses the NUL separated tokens of "log --raw -z" output with
// the "%x00%H" format: commit id tokens, then "<:meta>" and path tokens.
func parseChanges(toks []string) ([]Change, error) {
	var out []Change
	for i := 0; i < len(toks); i++ {
		t := strings.TrimPrefix(toks[i], "\n")
		switch {
		case t == "":
		case strings.HasPrefix(t, ":"):
			if len(out) == 0 || i+1 >= len(toks) {
				return nil, fmt.Errorf("unexpected git log output %q", t)
			}
			fc, err := parseRawEntry(t, toks[i+1])
			if err != nil {
				return nil, err
			}
			i++
			last := &out[len(out)-1]
			last.Files = append(last.Files, fc)
		case ValidID(t):
			out = append(out, Change{Commit: t})
		default:
			return nil, fmt.Errorf("unexpected git log output %q", t)
		}
	}
	return out, nil
}

// parseRawEntry parses ":<srcmode> <dstmode> <srcoid> <dstoid> <status>".
func parseRawEntry(meta, name string) (FileChange, error) {
	f := strings.Fields(meta)
	if len(f) != 5 || !ValidID(f[3]) && strings.Trim(f[3], "0") != "" {
		return FileChange{}, fmt.Errorf("unexpected git log entry %q", meta)
	}
	// Anything but a regular file (deletion, symlink, submodule) is not a
	// recipe: it counts as absent.
	if f[1] != "100644" && f[1] != "100755" {
		return FileChange{Name: name, Deleted: true}, nil
	}
	return FileChange{Name: name, OID: f[3]}, nil
}
