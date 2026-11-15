package buildconfig

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

// PathElem is one element of a repository path: a repository of a project, or
// an external URL repository identified by its stable "project/repository"
// name. Path order is search order: the consumer's own element first.
type PathElem struct {
	Project string
	Repo    string
	// URL is the external repository URL for url dependencies. It is not used
	// for layering; the element is looked up by Project like any other.
	URL string
}

// Loader returns the manifest of a project. A nil manifest with a nil error
// means the project is unknown; such path elements are skipped, as OBS does.
type Loader func(project string) (*manifest.ProjectManifest, error)

// Source is the configuration text a path element contributes, before layering.
type Source struct {
	Project string
	Repo    string
	Text    string
}

// PathFromTarget returns the path of target repo of prj: the target itself,
// followed by its dependencies in order. URL dependencies are identified by
// DependencySource.Name ("project/repository").
func PathFromTarget(prj *manifest.ProjectManifest, repo string) ([]PathElem, error) {
	path := []PathElem{{Project: prj.Project.Name, Repo: repo}}
	for _, d := range prj.Targets[repo].Dependencies {
		switch {
		case d.URL != "":
			p, r, ok := strings.Cut(d.Name, "/")
			if !ok || p == "" || r == "" {
				return nil, fmt.Errorf("url dependency %q of %s/%s needs name \"project/repository\"", d.URL, prj.Project.Name, repo)
			}
			path = append(path, PathElem{Project: p, Repo: r, URL: d.URL})
		case d.Project != "" && d.Target != "":
			path = append(path, PathElem{Project: d.Project, Repo: d.Target})
		default:
			return nil, fmt.Errorf("dependency of %s/%s needs project and target, or url", prj.Project.Name, repo)
		}
	}
	return path, nil
}

// Layer ports OBS getconfig (BSSched/ProjPacks.pm). It renders every path
// element for the Consumer Project, concatenates the texts in reverse path
// order and returns two texts: schedulerText with Macros: sections stripped
// and workerText with them kept (to be passed to build --dist). Neither is
// parsed here; parse with Parse. consumerRepo is the consumer's own
// repository; when path is empty the path defaults to the consumer's own
// element. Struct fields are never merged (ADR 0010).
func Layer(consumer, consumerRepo string, path []PathElem, load Loader) (schedulerText, workerText string, err error) {
	if len(path) == 0 {
		path = []PathElem{{Project: consumer, Repo: consumerRepo}}
	}
	var srcs []Source
	for i := len(path) - 1; i >= 0; i-- {
		el := path[i]
		prj, err := load(el.Project)
		if err != nil {
			return "", "", fmt.Errorf("load %s: %w", el.Project, err)
		}
		if prj == nil || !hasConfig(prj, el.Repo) {
			continue
		}
		text, err := Render(prj, el.Repo, "", consumer)
		if err != nil {
			return "", "", err
		}
		srcs = append(srcs, Source{Project: el.Project, Repo: el.Repo, Text: text})
	}
	schedulerText, workerText = LayerTexts(consumer, srcs)
	return schedulerText, workerText, nil
}

func hasConfig(prj *manifest.ProjectManifest, repo string) bool {
	if prj.GetPublicConfig() != nil || prj.GetProtectedConfig() != nil || prj.GetPrivateConfig() != nil {
		return true
	}
	if len(prj.ArchConfig) > 0 {
		return true
	}
	t, ok := prj.Targets[repo]
	return ok && (t.Config != nil || len(t.ArchConfig) > 0)
}

var (
	fromScratchRe   = regexp.MustCompile(`(?is)^(.*?)\nFromScratch:`)
	hasContentRe    = regexp.MustCompile(`\n[ \t]*[^\s#]`)
	macrosSectionRe = regexp.MustCompile(`(?ims)^[ \t]*macros:[ \t]*$(.*?)^[ \t]*:macros[ \t]*$`)
	macrosOpenEOFRe = regexp.MustCompile(`(?ims)^\s*macros:\s*$.*\z`)
)

// StripMacros removes the Macros: sections, keeping the line count, with the
// same regular expressions as getconfig.
func StripMacros(c string) string {
	var b strings.Builder
	last := 0
	for _, m := range macrosSectionRe.FindAllStringSubmatchIndex(c, -1) {
		b.WriteString(c[last:m[0]])
		b.WriteString(strings.Repeat("\n", strings.Count(c[m[2]:m[3]], "\n")))
		last = m[1]
	}
	b.WriteString(c[last:])
	return macrosOpenEOFRe.ReplaceAllString(b.String(), "")
}

// startsFromScratch reports whether c has a FromScratch: line that is
// preceded only by blank lines and comments.
func startsFromScratch(c string) bool {
	m := fromScratchRe.FindStringSubmatch("\n" + c)
	return m != nil && !hasContentRe.MatchString(m[1])
}

// LayerTexts is the text assembly of getconfig for already rendered path
// element texts. srcs must be in getconfig order, that is, the furthest
// dependency first and the consumer's own project last. It returns the
// scheduler text (Macros: stripped) and the worker text (Macros: kept).
// OBS-original prjconf text can be fed through it to build the reference of
// the round-trip test.
func LayerTexts(consumer string, srcs []Source) (schedulerText, workerText string) {
	initial := "%define _project " + consumer + "\n%define _obs_feature_exclude_cpu_constraints 1\n"
	sched, worker := initial, initial
	oldThis, oldIn := -1, -1
	for _, s := range srcs {
		if startsFromScratch(s.Text) {
			oldThis, oldIn = -1, -1
			sched, worker = initial, initial
		}
		head := "\n### from " + s.Project + "\n%define _repository " + s.Repo + "\n"
		newThis := b2i(s.Project == consumer)
		newIn := b2i(newThis == 1 || strings.HasPrefix(consumer, s.Project+":"))
		if newThis != oldThis {
			head += "%define _is_this_project " + strconv.Itoa(newThis) + "\n"
		}
		if newIn != oldIn {
			head += "%define _is_in_project " + strconv.Itoa(newIn) + "\n"
		}
		oldThis, oldIn = newThis, newIn
		head += "#!!line " + s.Project + ":0\n"
		sched += head + StripMacros(s.Text)
		worker += head + s.Text
	}
	return sched, worker
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
