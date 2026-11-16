package constraints

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseLines parses constraint lines as written in the project configuration
// ("Constraint: <path> [attr=value ...] <value>") and in "#!BuildConstraint:"
// recipe lines (BSDispatcher::Constraints::list2struct). The path is the
// colon separated element path of the _constraints schema, for example:
//
//	sandbox kvm
//	sandbox exclude=true chroot
//	hardware:processors 4
//	hardware:memory:size unit=G 16
//	hardware:cpu:flag avx2
//	linux:version:min 6.0
//
// List elements (hostlabel, sandbox, cpu flags) accumulate, scalar elements
// are replaced by later lines. The "+", "=" and "!" element modifiers of
// OBS are not supported and rejected.
func ParseLines(lines []string) (Constraints, error) {
	var c Constraints
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if err := applyLine(&c, f); err != nil {
			return Constraints{}, fmt.Errorf("bad constraint %q: %w", strings.TrimSpace(line), err)
		}
	}
	return c, nil
}

func applyLine(c *Constraints, f []string) error {
	path := strings.Split(f[0], ":")
	for _, p := range path {
		if p == "" || strings.ContainsAny(p[len(p)-1:], "+=!") {
			return fmt.Errorf("unsupported element %q", p)
		}
	}
	attrs, values := splitAttrs(f[1:])
	if len(values) == 0 {
		return fmt.Errorf("no value for %s", f[0])
	}
	switch path[0] {
	case "hostlabel", "sandbox":
		return applyMatches(c, path, attrs, values)
	case "linux":
		return applyLinux(&c.Linux, path, attrs, values)
	case "hardware":
		return applyHardware(&c.Hardware, path[1:], attrs, values)
	}
	return fmt.Errorf("unknown element %q", path[0])
}

// splitAttrs separates leading key=value attributes from the content words.
func splitAttrs(f []string) (map[string]string, []string) {
	attrs := map[string]string{}
	i := 0
	for ; i < len(f); i++ {
		k, v, ok := strings.Cut(f[i], "=")
		if !ok || k == "" {
			break
		}
		attrs[k] = v
	}
	return attrs, f[i:]
}

func matchesOf(attrs map[string]string, values []string) ([]Match, error) {
	for k := range attrs {
		if k != "exclude" {
			return nil, fmt.Errorf("unknown attribute %q", k)
		}
	}
	out := make([]Match, 0, len(values))
	for _, v := range values {
		out = append(out, Match{Value: v, Exclude: attrs["exclude"] == "true"})
	}
	return out, nil
}

func applyMatches(c *Constraints, path []string, attrs map[string]string, values []string) error {
	if len(path) != 1 {
		return fmt.Errorf("%s has no subelements", path[0])
	}
	m, err := matchesOf(attrs, values)
	if err != nil {
		return err
	}
	if path[0] == "sandbox" {
		c.Sandbox = append(c.Sandbox, m...)
	} else {
		c.HostLabels = append(c.HostLabels, m...)
	}
	return nil
}

func applyLinux(l *Linux, path []string, attrs map[string]string, values []string) error {
	if len(attrs) > 0 {
		return fmt.Errorf("unknown attribute in %s", strings.Join(path, ":"))
	}
	v := strings.Join(values, " ")
	switch strings.Join(path, ":") {
	case "linux:flavor":
		l.Flavor = v
	case "linux:version:min":
		l.Min = v
	case "linux:version:max":
		l.Max = v
	default:
		return fmt.Errorf("unknown element %q", strings.Join(path, ":"))
	}
	return nil
}

func applyHardware(h *Hardware, path []string, attrs map[string]string, values []string) error {
	switch {
	case len(path) == 0:
		return errors.New("hardware needs a subelement")
	case len(path) == 1:
		return applyHardwareCount(h, path[0], attrs, values)
	case len(path) == 2 && path[0] == "cpu" && path[1] == "flag":
		m, err := matchesOf(attrs, values)
		h.CPUFlags = append(h.CPUFlags, m...)
		return err
	case len(path) == 2 && path[1] == "size":
		return applyHardwareSize(h, path[0], attrs, values)
	}
	return fmt.Errorf("unknown element hardware:%s", strings.Join(path, ":"))
}

func applyHardwareSize(h *Hardware, name string, attrs map[string]string, values []string) error {
	for k := range attrs {
		if k != "unit" {
			return fmt.Errorf("unknown attribute %q", k)
		}
	}
	mb, err := sizeMB(name, attrs["unit"], strings.Join(values, " "))
	if err != nil {
		return err
	}
	switch name {
	case "disk":
		h.DiskMB = mb
	case "memory":
		h.MemoryMB = mb
	case "memoryperjob":
		h.MemoryPerJobMB = mb
	case "physicalmemory":
		h.PhysicalMemoryMB = mb
	default:
		return fmt.Errorf("unknown element hardware:%s:size", name)
	}
	return nil
}

func applyHardwareCount(h *Hardware, name string, attrs map[string]string, values []string) error {
	if len(attrs) > 0 {
		return fmt.Errorf("unknown attribute in hardware:%s", name)
	}
	n, err := parseCount(name, strings.Join(values, " "))
	if err != nil {
		return err
	}
	switch name {
	case "processors":
		h.Processors = n
	case "jobs":
		h.Jobs = n
	default:
		return fmt.Errorf("unknown element hardware:%s", name)
	}
	return nil
}

func parseCount(name, s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: invalid count %q", name, s)
	}
	return n, nil
}

// unitMB maps a size unit to its size in MiB (getmbsize). The default unit
// is bytes.
var unitMB = map[string]float64{
	"": 1.0 / (1024 * 1024), "B": 1.0 / (1024 * 1024), "K": 1.0 / 1024, "M": 1,
	"G": 1024, "T": 1024 * 1024, "P": 1024 * 1024 * 1024,
}

func sizeMB(name, unit, value string) (float64, error) {
	factor, ok := unitMB[strings.TrimSpace(unit)]
	if !ok {
		return 0, fmt.Errorf("%s: unknown size unit %q", name, unit)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s: invalid size %q", name, value)
	}
	return v * factor, nil
}
