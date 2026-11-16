package recipe

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// ServiceFile is the name of the source service definition in a package.
const ServiceFile = "_service"

// serviceMode is the mode of a source service that also runs in the build
// root, so its obs-service package must be installed there.
const serviceMode = "buildtime"

type servicesXML struct {
	XMLName xml.Name     `xml:"services"`
	Service []serviceXML `xml:"service"`
}

type serviceXML struct {
	Name string `xml:"name,attr"`
	Mode string `xml:"mode,attr"`
}

// BuildtimeServices returns the names of the services of a _service file that
// have mode="buildtime", in file order, like the buildtimeservice list of
// BSRepServer::BuildInfo and bs_srcserver. Other modes (localonly, manual,
// disabled, trylocal, serveronly, default) never add build dependencies.
func BuildtimeServices(data []byte) ([]string, error) {
	var x servicesXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ServiceFile, err)
	}
	var names []string
	for _, s := range x.Service {
		if s.Mode == serviceMode && s.Name != "" {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

// ServiceDeps maps buildtime service names to the packages the build root
// needs, like BSSched::BuildJob (btdeps): the project configuration
// substitution "obs-service:<name>" when defined, otherwise the package
// "obs-service-<name>" ("_" becomes "-" for deb). The result is unique and
// keeps the first-seen order.
func ServiceDeps(names []string, substitute map[string][]string, binaryType string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, n := range names {
		if sub, ok := substitute["obs-service:"+n]; ok {
			for _, p := range sub {
				add(p)
			}
			continue
		}
		pkg := "obs-service-" + n
		if binaryType == "deb" {
			pkg = strings.ReplaceAll(pkg, "_", "-")
		}
		add(pkg)
	}
	return out
}
