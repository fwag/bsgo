package constraints

import (
	"encoding/xml"
	"fmt"
	"strings"
)

type xmlMatch struct {
	Exclude string `xml:"exclude,attr"`
	Value   string `xml:",chardata"`
}

type xmlSize struct {
	Unit  string `xml:"unit,attr"`
	Value string `xml:",chardata"`
}

type xmlSizeElem struct {
	Size *xmlSize `xml:"size"`
}

type xmlHardware struct {
	CPU *struct {
		Flag []xmlMatch `xml:"flag"`
	} `xml:"cpu"`
	Processors     string       `xml:"processors"`
	Jobs           string       `xml:"jobs"`
	Disk           *xmlSizeElem `xml:"disk"`
	Memory         *xmlSizeElem `xml:"memory"`
	MemoryPerJob   *xmlSizeElem `xml:"memoryperjob"`
	PhysicalMemory *xmlSizeElem `xml:"physicalmemory"`
}

type xmlLinux struct {
	Flavor  string `xml:"flavor"`
	Version *struct {
		Min string `xml:"min"`
		Max string `xml:"max"`
	} `xml:"version"`
}

type xmlBody struct {
	HostLabel []xmlMatch   `xml:"hostlabel"`
	Sandbox   []xmlMatch   `xml:"sandbox"`
	Linux     *xmlLinux    `xml:"linux"`
	Hardware  *xmlHardware `xml:"hardware"`
}

type xmlOverwrite struct {
	xmlBody
	Conditions *struct {
		Arch    []string `xml:"arch"`
		Package []string `xml:"package"`
	} `xml:"conditions"`
}

type xmlDoc struct {
	XMLName xml.Name `xml:"constraints"`
	xmlBody
	Overwrite []xmlOverwrite `xml:"overwrite"`
}

// Overwrite is an <overwrite> section with its conditions.
type Overwrite struct {
	Arch, Package []string
	Constraints   Constraints
}

// Document is a parsed _constraints file.
type Document struct {
	Constraints Constraints
	Overwrites  []Overwrite
}

// ParseXML parses a _constraints file.
func ParseXML(data []byte) (*Document, error) {
	var x xmlDoc
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("bad constraints: %w", err)
	}
	base, err := x.convert()
	if err != nil {
		return nil, fmt.Errorf("bad constraints: %w", err)
	}
	doc := &Document{Constraints: base}
	for i := range x.Overwrite {
		o := &x.Overwrite[i]
		if o.Conditions == nil {
			continue // OBS ignores overwrite sections without conditions
		}
		c, err := o.convert()
		if err != nil {
			return nil, fmt.Errorf("bad constraints: overwrite: %w", err)
		}
		doc.Overwrites = append(doc.Overwrites, Overwrite{Arch: o.Conditions.Arch, Package: o.Conditions.Package, Constraints: c})
	}
	return doc, nil
}

// For applies the overwrite sections whose conditions match arch and package
// (overwriteconstraints): an arch list must contain arch, a package list the
// package name or its short name (cut at the first dot).
func (d *Document) For(arch, pkg string) Constraints {
	c := d.Constraints
	short, _, _ := strings.Cut(pkg, ".")
	for _, o := range d.Overwrites {
		if len(o.Arch) > 0 && !contains(o.Arch, arch) {
			continue
		}
		if len(o.Package) > 0 && !contains(o.Package, pkg) && !contains(o.Package, short) {
			continue
		}
		c = overwrite(c, o.Constraints)
	}
	return c
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if strings.TrimSpace(e) == s {
			return true
		}
	}
	return false
}

func (b *xmlBody) convert() (Constraints, error) {
	c := Constraints{HostLabels: convertMatches(b.HostLabel), Sandbox: convertMatches(b.Sandbox)}
	if b.Linux != nil {
		c.Linux.Flavor = strings.TrimSpace(b.Linux.Flavor)
		if v := b.Linux.Version; v != nil {
			c.Linux.Min, c.Linux.Max = strings.TrimSpace(v.Min), strings.TrimSpace(v.Max)
		}
	}
	if b.Hardware == nil {
		return c, nil
	}
	var err error
	c.Hardware, err = b.Hardware.convert()
	return c, err
}

func (h *xmlHardware) convert() (Hardware, error) {
	var out Hardware
	if h.CPU != nil {
		out.CPUFlags = convertMatches(h.CPU.Flag)
	}
	var err error
	if out.Processors, err = parseCount("processors", h.Processors); err != nil {
		return out, err
	}
	if out.Jobs, err = parseCount("jobs", h.Jobs); err != nil {
		return out, err
	}
	for _, s := range []struct {
		name string
		in   *xmlSizeElem
		out  *float64
	}{
		{"disk", h.Disk, &out.DiskMB},
		{"memory", h.Memory, &out.MemoryMB},
		{"memoryperjob", h.MemoryPerJob, &out.MemoryPerJobMB},
		{"physicalmemory", h.PhysicalMemory, &out.PhysicalMemoryMB},
	} {
		if s.in == nil || s.in.Size == nil {
			continue
		}
		if *s.out, err = sizeMB(s.name, s.in.Size.Unit, s.in.Size.Value); err != nil {
			return out, err
		}
	}
	return out, nil
}

func convertMatches(in []xmlMatch) []Match {
	if len(in) == 0 {
		return nil
	}
	out := make([]Match, 0, len(in))
	for _, m := range in {
		out = append(out, Match{Value: strings.TrimSpace(m.Value), Exclude: strings.TrimSpace(m.Exclude) == "true"})
	}
	return out
}
