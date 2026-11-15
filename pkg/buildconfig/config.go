// Package buildconfig parses OBS build configurations (project configs,
// "prjconf") into a structured Config. It is a behavior-compatible Go port of
// obs-build's Build::read_config, including the subset of rpm macro
// processing (%define, %if, %{?x}, %{expand:...}, ...) that read_config
// relies on. Parity with the Perl implementation is verified by the oracle
// test in test/oracle and by golden files in test/fixtures/prjconf/golden.
package buildconfig

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// Config mirrors the hash returned by Build::read_config. The JSON field
// names equal the Perl hash keys (flag helper hashes such as
// "expandflags:foo" are grouped into the *_h maps).
type Config struct {
	// Macros is the rpm macro definition list: the standard macros, the
	// _target_cpu/_target_os defines, DistMacro/Target additions and finally
	// the lines of the Macros: blocks (see RawMacros).
	Macros []string `json:"macros"`
	// Arch is the architecture the config was parsed for.
	Arch string `json:"arch"`

	// Preinstall lists packages installed first into the build root.
	Preinstall []string `json:"preinstall"`
	// Vminstall lists packages installed into VM build roots.
	Vminstall []string `json:"vminstall"`
	// Runscripts lists preinstalled packages whose scripts are run.
	Runscripts []string `json:"runscripts"`
	// Required lists packages always required in the build root.
	Required []string `json:"required"`
	// Support lists support packages for the build environment.
	Support []string `json:"support"`
	// Keep lists packages kept in the build root during cleanup.
	Keep []string `json:"keep"`
	// Prefer lists packages preferred when resolving ambiguous providers;
	// "!pkg" entries are Prefer-negations kept as such by the solver.
	Prefer []string `json:"prefer"`
	// Ignore lists dependencies ignored during resolution (pkg or pkg:dep).
	Ignore []string `json:"ignore"`
	// Conflict lists declared package conflicts (a, a:b or a,b forms).
	Conflict []string `json:"conflict"`
	// IgnoreRebuild lists packages not triggering rebuilds.
	IgnoreRebuild []string `json:"ignorerebuild"`
	// AlsoNative lists packages also needed from the native architecture.
	AlsoNative []string `json:"alsonative"`
	// OnlyNative lists packages only needed from the native architecture.
	OnlyNative []string `json:"onlynative"`

	// Substitute maps a virtual dependency to the packages replacing it.
	// Trailing "=" markers (version propagation) are stripped; see
	// SubstituteVers for the original form.
	Substitute map[string][]string `json:"substitute"`
	// SubstituteVers holds, per Substitute key, a flat list of
	// (name, marker) pairs where marker is "=" or null.
	SubstituteVers map[string][]*string `json:"substitute_vers"`
	// Optflags maps an architecture (or "*") to compiler flags.
	Optflags map[string]string `json:"optflags"`
	// Order is the set of packages with a fixed install order.
	Order map[string]bool `json:"order"`
	// ExportFilter maps a regular expression to the architectures that
	// receive matching binaries.
	ExportFilter map[string][]string `json:"exportfilter"`
	// PublishFilter lists regular expressions of binaries not published.
	PublishFilter []string `json:"publishfilter"`
	// RawMacros is the verbatim text of all Macros: blocks.
	RawMacros string `json:"rawmacros"`
	// Release is the release number template; defaults to "<CI_CNT>.<B_CNT>".
	Release string `json:"release"`
	// ReleaseList holds all fields of the Release: line.
	ReleaseList []string `json:"release@"`
	// Repotype lists the repository metadata formats to generate.
	Repotype []string `json:"repotype"`
	// Patterntype lists the pattern formats to generate.
	Patterntype []string `json:"patterntype"`
	// FileProvides maps a file path to the packages providing it.
	FileProvides map[string][]string `json:"fileprovides"`
	// Constraint lists build constraint lines.
	Constraint []string `json:"constraint"`
	// ExpandFlags, BuildFlags and PublishFlags list the raw flag words.
	ExpandFlags  []string `json:"expandflags"`
	BuildFlags   []string `json:"buildflags"`
	PublishFlags []string `json:"publishflags"`
	// SingleExport avoids exporting multiple package containers.
	SingleExport string `json:"singleexport"`
	// RepoURL, RegistryURL and AssetsURL list configured URLs.
	RepoURL     []string `json:"repourl"`
	RegistryURL []string `json:"registryurl"`
	AssetsURL   []string `json:"assetsurl"`

	// Type is the recipe system (spec, dsc, arch, kiwi, ...); guessed from
	// Preinstall when not set, "UNDEFINED" when unknown.
	Type string `json:"type"`
	// BuildEngine is the build engine (build, mock).
	BuildEngine string `json:"buildengine"`
	// BinaryType is the binary package format (rpm, deb, arch, apk).
	BinaryType string `json:"binarytype"`
	// CICntStart, ReleasePrg, ReleaseSuffix, Target and HostArch hold the
	// like-named scalar directives (empty when unset).
	CICntStart    string `json:"cicntstart"`
	ReleasePrg    string `json:"releaseprg"`
	ReleaseSuffix string `json:"releasesuffix"`
	Target        string `json:"target"`
	HostArch      string `json:"hostarch"`

	// PreferH, IgnoreH, ConflictH are derived lookup tables
	// (Build::init_helper_hashes).
	PreferH   map[string]bool     `json:"preferh"`
	IgnoreH   map[string]bool     `json:"ignoreh"`
	ConflictH map[string][]string `json:"conflicth"`
	// ExpandFlagsH, BuildFlagsH and PublishFlagsH expand the flag lists:
	// "name:value" yields name=value, a bare "name" yields name="1".
	ExpandFlagsH  map[string]string `json:"expandflags_h"`
	BuildFlagsH   map[string]string `json:"buildflags_h"`
	PublishFlagsH map[string]string `json:"publishflags_h"`
	// Modules is the sorted set of values of ExpandFlags "module:" entries.
	Modules []string `json:"modules"`

	// ParseError is the first error found while parsing, if any.
	ParseError string `json:"parse_error"`
	// Warnings collects non-fatal diagnostics (obs-build prints them to
	// stderr). It is not part of the compared result.
	Warnings []string `json:"-"`
}

func newConfig(arch string) *Config {
	macros := splitRe(newlineRe, stdMacros)
	macros = append(macros, "%define _target_cpu "+arch, "%define _target_os linux")
	return &Config{
		Macros: macros, Arch: arch,
		Preinstall: []string{}, Vminstall: []string{}, Runscripts: []string{},
		Required: []string{}, Support: []string{}, Keep: []string{},
		Prefer: []string{}, Ignore: []string{}, Conflict: []string{},
		IgnoreRebuild: []string{}, AlsoNative: []string{}, OnlyNative: []string{},
		Substitute: map[string][]string{}, SubstituteVers: map[string][]*string{},
		Optflags: map[string]string{}, Order: map[string]bool{},
		ExportFilter: map[string][]string{}, PublishFilter: []string{},
		Release: "<CI_CNT>.<B_CNT>", ReleaseList: []string{},
		Repotype: []string{}, Patterntype: []string{},
		FileProvides: map[string][]string{}, Constraint: []string{},
		ExpandFlags: []string{}, BuildFlags: []string{}, PublishFlags: []string{},
		RepoURL: []string{}, RegistryURL: []string{}, AssetsURL: []string{},
		PreferH: map[string]bool{}, IgnoreH: map[string]bool{},
		ConflictH:    map[string][]string{},
		ExpandFlagsH: map[string]string{}, BuildFlagsH: map[string]string{},
		PublishFlagsH: map[string]string{}, Modules: []string{},
	}
}

// Parse parses the build configuration text for the given architecture,
// like Build::read_config($arch, \@lines). The returned Config is always
// non-nil; the error is non-nil when ParseError is set (for example for a
// macro expression error or a misplaced FromScratch directive).
func Parse(text, arch string) (*Config, error) {
	c := newConfig(arch)
	st := &parseState{}
	eng := newMacroEngine(c.Macros, st.warn)
	p := &specParser{st: st, eng: eng}
	lines := p.run(collectMacroBlobs(splitLines(text)))
	c.ParseError = st.parseError
	d := &directives{cfg: c}
	for _, l := range lines {
		if !l.undef {
			d.apply(l.text)
		}
	}
	finalize(c)
	if c.ParseError != "" {
		return c, errors.New(c.ParseError)
	}
	return c, nil
}

var (
	ignoreSplitRe = regexp.MustCompile(`[,:]`)
	flagRe        = regexp.MustCompile(`(?s)^([^:]+):(.*)$`)
)

var repoCreationDefaults = map[string][]string{
	"rpm":  {"createrepo"},
	"deb":  {"dpkg-dev"},
	"arch": {"pacman"},
	"apk":  {"apk-tools"},
}

var nativeTypeBinary = map[string]string{
	"spec": "rpm", "dsc": "deb", "collax": "deb", "livebuild": "deb",
	"arch": "arch", "apk": "apk",
}

func finalize(c *Config) {
	for _, l := range []*[]string{
		&c.Preinstall, &c.Vminstall, &c.Required, &c.Support, &c.Keep,
		&c.Runscripts, &c.Repotype, &c.Patterntype, &c.IgnoreRebuild,
	} {
		*l = unify(*l)
	}
	initHelperHashes(c)
	setTypes(c)
	finalizeSubstitutes(c)
	addMacros(c, c.RawMacros)
	c.ExpandFlagsH, c.Modules = flagHash(c.ExpandFlags, true)
	c.BuildFlagsH, _ = flagHash(c.BuildFlags, false)
	c.PublishFlagsH, _ = flagHash(c.PublishFlags, false)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// setTypes derives Type and BinaryType from the preinstall list when they
// were not configured explicitly.
func setTypes(c *Config) {
	if !perlTrue(c.Type) {
		switch {
		case contains(c.Preinstall, "rpm"):
			c.Type = "spec"
		case contains(c.Preinstall, "debianutils"):
			c.Type = "dsc"
		case contains(c.Preinstall, "pacman"):
			c.Type = "arch"
		case contains(c.Preinstall, "apk-tools"):
			c.Type = "apk"
		}
		if !perlTrue(c.Type) {
			c.Type = "UNDEFINED"
		}
	}
	if perlTrue(c.BinaryType) {
		return
	}
	c.BinaryType = nativeTypeBinary[c.Type]
	switch c.Type {
	case "snapcraft", "appimage", "docker", "fissile", "kiwi", "mkosi":
		switch {
		case contains(c.Preinstall, "rpm"):
			c.BinaryType = "rpm"
		case contains(c.Preinstall, "debianutils"):
			c.BinaryType = "deb"
		case contains(c.Preinstall, "pacman"):
			c.BinaryType = "arch"
		}
	}
	if !perlTrue(c.BinaryType) {
		c.BinaryType = "UNDEFINED"
	}
}

func finalizeSubstitutes(c *Config) {
	const key = "system-packages:repo-creation"
	if _, ok := c.Substitute[key]; !ok {
		if def, ok := repoCreationDefaults[c.BinaryType]; ok {
			c.Substitute[key] = append([]string{}, def...)
		}
	}
	for k, v := range c.Substitute {
		u := unify(v)
		vers := make([]*string, 0, 2*len(u))
		stripped := make([]string, len(u))
		for i, item := range u {
			name, marker := strings.TrimSuffix(item, "="), (*string)(nil)
			if name != item {
				eq := "="
				marker = &eq
			}
			n := name
			vers = append(vers, &n, marker)
			stripped[i] = name
		}
		c.SubstituteVers[k] = vers
		c.Substitute[k] = stripped
	}
}

// initHelperHashes ports Build::init_helper_hashes.
func initHelperHashes(c *Config) {
	for _, p := range c.Prefer {
		c.PreferH[p] = true
	}
	for _, ign := range c.Ignore {
		if !strings.Contains(ign, ":") {
			c.IgnoreH[ign] = true
			continue
		}
		s := splitRe(ignoreSplitRe, ign)
		if len(s) == 0 {
			continue
		}
		for _, x := range s[1:] {
			c.IgnoreH[s[0]+":"+x] = true
		}
	}
	for _, cf := range c.Conflict {
		addConflict(c.ConflictH, splitRe(ignoreSplitRe, cf))
	}
	for k, v := range c.ConflictH {
		c.ConflictH[k] = unify(v)
	}
}

func addConflict(h map[string][]string, s []string) {
	if len(s) == 0 {
		return
	}
	first, rest := s[0], s[1:]
	if perlTrue(first) && len(rest) == 0 {
		h[":"] = append(h[":"], first)
	}
	if len(rest) > 0 {
		h[first] = append(h[first], rest...)
	}
	for _, r := range rest {
		h[r] = append(h[r], first)
	}
}

// flagHash expands "name:value" and bare "name" flag words. When modules is
// set it also returns the sorted set of module values.
func flagHash(flags []string, modules bool) (map[string]string, []string) {
	h := map[string]string{}
	mods := map[string]bool{}
	for _, f := range flags {
		m := flagRe.FindStringSubmatch(f)
		if m == nil {
			h[f] = "1"
			continue
		}
		h[m[1]] = m[2]
		if modules && m[1] == "module" {
			mods[m[2]] = true
		}
	}
	list := make([]string, 0, len(mods))
	for m := range mods {
		list = append(list, m)
	}
	slices.Sort(list)
	return h, list
}

// addMacros ports Build::Rpm::add_macros.
func addMacros(c *Config, raw string) {
	for _, rm := range splitRe(newlineRe, raw) {
		last := len(c.Macros) - 1
		switch {
		case last >= 0 && strings.HasSuffix(c.Macros[last], "\\"):
			c.Macros[last] = c.Macros[last][:len(c.Macros[last])-1] + "\n" + rm
		case !strings.HasPrefix(rm, "%"):
			c.Macros = append(c.Macros, rm)
		default:
			c.Macros = append(c.Macros, "%define "+rm[1:])
		}
	}
}
