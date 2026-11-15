package recipe

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// MultibuildFile is the name of the multibuild definition in a package.
const MultibuildFile = "_multibuild"

// Multibuild is a parsed _multibuild file.
type Multibuild struct {
	// BuildEmptyFlavor is false when the plain package must not be built
	// (buildemptyflavor="false" or "0"). It defaults to true.
	BuildEmptyFlavor bool
	// Flavors lists the flavor names in file order. <flavor> and <package>
	// elements are equivalent; <flavor> wins when both are present like in
	// Multibuild::addmultibuildpackages.
	Flavors []string
}

type multibuildXML struct {
	XMLName          xml.Name `xml:"multibuild"`
	BuildEmptyFlavor *string  `xml:"buildemptyflavor,attr"`
	Flavor           []string `xml:"flavor"`
	Package          []string `xml:"package"`
}

var flavorNameRe = regexp.MustCompile(`^[^:/\s]+$`)

// ParseMultibuild parses and verifies a _multibuild document like
// BSVerify::verify_multibuild.
func ParseMultibuild(data []byte) (*Multibuild, error) {
	var x multibuildXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse %s: %w", MultibuildFile, err)
	}
	mb := &Multibuild{BuildEmptyFlavor: true}
	if v := x.BuildEmptyFlavor; v != nil {
		switch strings.TrimSpace(*v) {
		case "true", "1":
		case "false", "0":
			mb.BuildEmptyFlavor = false
		default:
			return nil, fmt.Errorf("%s: 'buildemptyflavor' attribute must be boolean", MultibuildFile)
		}
	}
	if len(x.Flavor) > 0 && len(x.Package) > 0 {
		return nil, fmt.Errorf("%s: multibuild cannot have both package and flavor elements", MultibuildFile)
	}
	names := x.Flavor
	if len(names) == 0 {
		names = x.Package
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if !flavorNameRe.MatchString(n) {
			return nil, fmt.Errorf("%s: flavor %q is illegal in multibuild", MultibuildFile, n)
		}
		mb.Flavors = append(mb.Flavors, n)
	}
	return mb, nil
}

// Unit is one buildable package identity: the plain package or one flavor.
type Unit struct {
	// Name is "pkg" or "pkg:flavor"; this is the identity used everywhere.
	Name string
	// Package is the base package name.
	Package string
	// Flavor is empty for the plain package.
	Flavor string
	// RecipeFile is the selected recipe, empty when none matches (OBS reports
	// such units as excluded).
	RecipeFile string
}

// Expand ports Multibuild::addmultibuildpackages plus bs_srcserver findfile
// for spec recipes: it returns the plain package (unless buildemptyflavor is
// false) followed by one unit per flavor, each with its recipe chosen with
// the flavor as lookup name. repo is the repository name used for
// "<name>-<repo>.spec"; empty means none. mb may be nil.
func Expand(packageName string, files []string, mb *Multibuild, repo string) []Unit {
	var units []Unit
	if mb == nil || mb.BuildEmptyFlavor {
		units = append(units, Unit{
			Name: packageName, Package: packageName,
			RecipeFile: FindRecipe(packageName, repo, "spec", files),
		})
	}
	if mb == nil {
		return units
	}
	for _, f := range mb.Flavors {
		units = append(units, Unit{
			Name: packageName + ":" + f, Package: packageName, Flavor: f,
			RecipeFile: FindRecipe(f, repo, "spec", files),
		})
	}
	return units
}

// FindRecipe ports bs_srcserver findfile for the rpm-style extensions: lookup
// is the package name, or the flavor for a multibuild unit. It returns "" when
// no recipe matches.
func FindRecipe(lookup, repo, ext string, files []string) string {
	has := make(map[string]bool, len(files))
	for _, f := range files {
		has[f] = true
	}
	if repo != "" && has[lookup+"-"+repo+"."+ext] {
		return lookup + "-" + repo + "." + ext
	}
	if has[lookup+"."+ext] {
		return lookup + "." + ext
	}
	if strings.Contains(lookup, ".") {
		p := lookup
		for {
			i := strings.LastIndexByte(p, '.')
			if i < 0 {
				break
			}
			p = p[:i]
			if has[p+"."+ext] {
				return p + "." + ext
			}
		}
	}
	return fallbackRecipe(lookup, repo, ext, files)
}

// fallbackRecipe picks among the remaining *.ext files by name prefix.
func fallbackRecipe(lookup, repo, ext string, files []string) string {
	var cand []string
	for _, f := range files {
		if strings.HasSuffix(f, "."+ext) {
			cand = append(cand, f)
		}
	}
	if len(cand) > 1 {
		cand = filterPrefix(cand, lookup)
	}
	if len(cand) == 1 {
		return cand[0]
	}
	if len(cand) > 1 {
		if repo == "" {
			slices.Sort(cand)
			return cand[0]
		}
		cand = filterPrefix(cand, lookup+"-"+repo)
		if len(cand) == 1 {
			return cand[0]
		}
	}
	return ""
}

// filterPrefix keeps the names starting with prefix, case-insensitively.
func filterPrefix(names []string, prefix string) []string {
	var out []string
	for _, n := range names {
		if len(n) >= len(prefix) && strings.EqualFold(n[:len(prefix)], prefix) {
			out = append(out, n)
		}
	}
	return out
}
