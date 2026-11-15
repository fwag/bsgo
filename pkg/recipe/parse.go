package recipe

import (
	"fmt"
	"path"
	"strings"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

// recipeType maps a recipe file name to its build type like
// Build::recipe2buildtype; "" when unknown.
func recipeType(file string) string {
	base := path.Base(file)
	switch {
	case strings.HasSuffix(base, ".spec"):
		return "spec"
	case strings.HasSuffix(base, ".dsc"), base == "debian.control":
		return "dsc"
	case strings.HasSuffix(base, ".kiwi"), base == "config.xml":
		return "kiwi"
	case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile."):
		return "docker"
	case strings.HasSuffix(base, ".productcompose"):
		return "productcompose"
	case base == "PKGBUILD":
		return "arch"
	case base == "APKBUILD":
		return "apk"
	}
	return ""
}

// Parse parses the recipe at recipePath with the macros of the layered build
// configuration cfg (the full getconfig text including its Macros: blocks).
// Only spec recipes are supported; other known types return an
// *UnsupportedError and unknown ones a plain error.
func Parse(cfg *buildconfig.Config, recipePath string, content []byte, opt Options) (*Info, error) {
	switch typ := recipeType(recipePath); typ {
	case "spec":
		return parseSpec(cfg, recipePath, string(content), opt), nil
	case "":
		return nil, fmt.Errorf("don't know how to build %s", recipePath)
	default:
		return nil, &UnsupportedError{Path: recipePath, Type: typ}
	}
}
