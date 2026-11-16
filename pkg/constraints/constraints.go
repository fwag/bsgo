// Package constraints ports the OBS build job constraints
// (BSDispatcher::Constraints, BSXML::constraints): the per-package
// _constraints file, the #!BuildConstraint lines of a recipe and the
// "Constraint:" lines of the project configuration, merged the way
// bs_dispatch merges them.
//
// Not every field is enforced by the dispatcher: sandbox, memory, disk and
// processors are. hostlabel, linux, jobs, memoryperjob, physicalmemory and
// the cpu flags are parsed and merged but the workers do not report the
// matching facts yet, so they are ignored at dispatch time.
package constraints

import "strings"

// OverrideSandbox replaces the sandbox constraint of c with spec, the
// bsgo-native override of the package manifest (build.constraints.sandbox).
// spec is a space separated list of sandbox names; a leading "!" excludes a
// sandbox. The single word "none" removes the sandbox constraint. An empty
// spec leaves c unchanged.
func OverrideSandbox(c Constraints, spec string) Constraints {
	words := strings.Fields(spec)
	switch {
	case len(words) == 0:
		return c
	case len(words) == 1 && words[0] == "none":
		c.Sandbox = nil
		return c
	}
	c.Sandbox = make([]Match, 0, len(words))
	for _, w := range words {
		name, excl := strings.CutPrefix(w, "!")
		c.Sandbox = append(c.Sandbox, Match{Value: name, Exclude: excl})
	}
	return c
}

// Match is one value of a list constraint with its optional exclusion.
type Match struct {
	Value   string `json:"value"`
	Exclude bool   `json:"exclude,omitempty"`
}

// Linux constrains the worker kernel.
type Linux struct {
	Flavor string `json:"flavor,omitempty"`
	// Min and Max bound the kernel version.
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
}

// Hardware holds the hardware constraints. Zero means unset; sizes are MiB.
type Hardware struct {
	Processors       int     `json:"processors,omitempty"`
	Jobs             int     `json:"jobs,omitempty"`
	DiskMB           float64 `json:"disk_mb,omitempty"`
	MemoryMB         float64 `json:"memory_mb,omitempty"`
	MemoryPerJobMB   float64 `json:"memoryperjob_mb,omitempty"`
	PhysicalMemoryMB float64 `json:"physicalmemory_mb,omitempty"`
	CPUFlags         []Match `json:"cpu_flags,omitempty"`
}

// Constraints is a merged constraint set.
type Constraints struct {
	HostLabels []Match  `json:"hostlabels,omitempty"`
	Sandbox    []Match  `json:"sandbox,omitempty"`
	Linux      Linux    `json:"linux,omitempty"`
	Hardware   Hardware `json:"hardware,omitempty"`
}

// Merge returns base with over merged in (BSDispatcher::Constraints::
// mergeconstraints): list constraints (hostlabel, sandbox, cpu flags) are
// concatenated, base first; scalar constraints set in over replace those of
// base.
func Merge(base, over Constraints) Constraints {
	out := base
	out.HostLabels = concatMatches(base.HostLabels, over.HostLabels)
	out.Sandbox = concatMatches(base.Sandbox, over.Sandbox)
	out.Linux = overlayLinux(base.Linux, over.Linux)
	out.Hardware = overlayHardware(base.Hardware, over.Hardware)
	out.Hardware.CPUFlags = concatMatches(base.Hardware.CPUFlags, over.Hardware.CPUFlags)
	return out
}

func concatMatches(a, b []Match) []Match {
	if len(a)+len(b) == 0 {
		return nil
	}
	return append(append([]Match(nil), a...), b...)
}

func overlayLinux(base, over Linux) Linux {
	if over.Flavor != "" {
		base.Flavor = over.Flavor
	}
	if over.Min != "" {
		base.Min = over.Min
	}
	if over.Max != "" {
		base.Max = over.Max
	}
	return base
}

// overlayHardware replaces the scalar fields of base that over sets; the
// cpu flag list of base is kept as is.
func overlayHardware(base, over Hardware) Hardware {
	if over.Processors != 0 {
		base.Processors = over.Processors
	}
	if over.Jobs != 0 {
		base.Jobs = over.Jobs
	}
	if over.DiskMB != 0 {
		base.DiskMB = over.DiskMB
	}
	if over.MemoryMB != 0 {
		base.MemoryMB = over.MemoryMB
	}
	if over.MemoryPerJobMB != 0 {
		base.MemoryPerJobMB = over.MemoryPerJobMB
	}
	if over.PhysicalMemoryMB != 0 {
		base.PhysicalMemoryMB = over.PhysicalMemoryMB
	}
	return base
}

// overwrite applies an <overwrite> section (BSDispatcher::Constraints::
// overwrite): unlike Merge, list constraints that o sets replace the lists of
// c instead of extending them.
func overwrite(c, o Constraints) Constraints {
	if o.HostLabels != nil {
		c.HostLabels = o.HostLabels
	}
	if o.Sandbox != nil {
		c.Sandbox = o.Sandbox
	}
	flags := c.Hardware.CPUFlags
	if o.Hardware.CPUFlags != nil {
		flags = o.Hardware.CPUFlags
	}
	c.Linux = overlayLinux(c.Linux, o.Linux)
	c.Hardware = overlayHardware(c.Hardware, o.Hardware)
	c.Hardware.CPUFlags = flags
	return c
}

// Sources are the inputs of Resolve.
type Sources struct {
	// XML is the content of the package's _constraints file, nil when absent.
	XML []byte
	// Recipe are the #!BuildConstraint lines of the recipe.
	Recipe []string
	// Prjconf are the "Constraint:" lines of the project configuration.
	Prjconf []string
	// Arch and Package select the <overwrite> sections of XML. Package is
	// the unit name ("pkg" or "pkg:flavor").
	Arch, Package string
}

// Resolve merges the three constraint sources like bs_dispatch: the
// _constraints file with its matching overwrite sections, then the recipe
// lines on top of it, then the project configuration below both (package
// constraints win over project constraints).
func Resolve(src Sources) (Constraints, error) {
	var pkg Constraints
	if len(src.XML) > 0 {
		doc, err := ParseXML(src.XML)
		if err != nil {
			return Constraints{}, err
		}
		pkg = doc.For(src.Arch, src.Package)
	}
	if len(src.Recipe) > 0 {
		rc, err := ParseLines(src.Recipe)
		if err != nil {
			return Constraints{}, err
		}
		pkg = Merge(pkg, rc)
	}
	if len(src.Prjconf) == 0 {
		return pkg, nil
	}
	pc, err := ParseLines(src.Prjconf)
	if err != nil {
		return Constraints{}, err
	}
	return Merge(pc, pkg), nil
}
