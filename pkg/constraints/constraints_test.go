package constraints

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseLines(t *testing.T) {
	tests := []struct {
		name    string
		lines   []string
		want    Constraints
		wantErr string
	}{
		{"sandbox", []string{"sandbox kvm"}, Constraints{Sandbox: []Match{{Value: "kvm"}}}, ""},
		{"sandbox exclude", []string{"sandbox exclude=true chroot"}, Constraints{Sandbox: []Match{{Value: "chroot", Exclude: true}}}, ""},
		{"sandbox accumulates", []string{"sandbox kvm", "sandbox xen"},
			Constraints{Sandbox: []Match{{Value: "kvm"}, {Value: "xen"}}}, ""},
		{"processors replaced", []string{"hardware:processors 4", "hardware:processors 8"},
			Constraints{Hardware: Hardware{Processors: 8}}, ""},
		{"memory GiB", []string{"hardware:memory:size unit=G 16"}, Constraints{Hardware: Hardware{MemoryMB: 16384}}, ""},
		{"disk default unit bytes", []string{"hardware:disk:size 1048576"}, Constraints{Hardware: Hardware{DiskMB: 1}}, ""},
		{"cpu flags", []string{"hardware:cpu:flag avx2 sse4_2", "hardware:cpu:flag exclude=true nol0"},
			Constraints{Hardware: Hardware{CPUFlags: []Match{{Value: "avx2"}, {Value: "sse4_2"}, {Value: "nol0", Exclude: true}}}}, ""},
		{"linux", []string{"linux:flavor default", "linux:version:min 6.0", "linux:version:max 7.0"},
			Constraints{Linux: Linux{Flavor: "default", Min: "6.0", Max: "7.0"}}, ""},
		{"hostlabel", []string{"hostlabel bench"}, Constraints{HostLabels: []Match{{Value: "bench"}}}, ""},
		{"blank line", []string{"  "}, Constraints{}, ""},
		{"unknown element", []string{"frobnicate 1"}, Constraints{}, "unknown element"},
		{"modifier", []string{"sandbox+ kvm"}, Constraints{}, "unsupported element"},
		{"no value", []string{"sandbox"}, Constraints{}, "no value"},
		{"bad unit", []string{"hardware:memory:size unit=X 1"}, Constraints{}, "unknown size unit"},
		{"bad size", []string{"hardware:memory:size unit=G lots"}, Constraints{}, "invalid size"},
		{"bad count", []string{"hardware:processors many"}, Constraints{}, "invalid count"},
		{"bad attribute", []string{"sandbox foo=bar kvm"}, Constraints{}, "unknown attribute"},
		{"subelement of sandbox", []string{"sandbox:x kvm"}, Constraints{}, "no subelements"},
		{"unknown hardware", []string{"hardware:gpu 1"}, Constraints{}, "unknown element"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLines(tt.lines)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

const xmlDocument = `<constraints>
  <hostlabel>bench</hostlabel>
  <sandbox exclude="true">chroot</sandbox>
  <linux><version><max>3.42</max><min>1.0</min></version><flavor>default</flavor></linux>
  <hardware>
    <cpu><flag>fpu</flag><flag exclude="true">nol0kernel</flag></cpu>
    <processors>2</processors>
    <disk><size unit="T">1</size></disk>
    <memory><size unit="G">1.5</size></memory>
  </hardware>
  <overwrite>
    <conditions><arch>x86_64</arch><package>kernel-default</package></conditions>
    <sandbox>kvm</sandbox>
    <hardware><processors>8</processors><disk><size unit="G">2</size></disk></hardware>
  </overwrite>
  <overwrite>
    <sandbox>xen</sandbox>
  </overwrite>
</constraints>`

func TestParseXML(t *testing.T) {
	doc, err := ParseXML([]byte(xmlDocument))
	if err != nil {
		t.Fatal(err)
	}
	want := Constraints{
		HostLabels: []Match{{Value: "bench"}},
		Sandbox:    []Match{{Value: "chroot", Exclude: true}},
		Linux:      Linux{Flavor: "default", Min: "1.0", Max: "3.42"},
		Hardware: Hardware{
			Processors: 2, DiskMB: 1024 * 1024, MemoryMB: 1536,
			CPUFlags: []Match{{Value: "fpu"}, {Value: "nol0kernel", Exclude: true}},
		},
	}
	if !reflect.DeepEqual(doc.Constraints, want) {
		t.Fatalf("got %+v, want %+v", doc.Constraints, want)
	}
	if len(doc.Overwrites) != 1 {
		t.Fatalf("overwrite without conditions must be dropped, got %d overwrites", len(doc.Overwrites))
	}
}

func TestParseXMLErrors(t *testing.T) {
	for name, in := range map[string]string{
		"not xml":       "<constraints",
		"bad size":      `<constraints><hardware><memory><size unit="G">x</size></memory></hardware></constraints>`,
		"bad unit":      `<constraints><hardware><disk><size unit="Q">1</size></disk></hardware></constraints>`,
		"bad jobs":      `<constraints><hardware><jobs>-1</jobs></hardware></constraints>`,
		"bad overwrite": `<constraints><overwrite><conditions/><hardware><processors>z</processors></hardware></overwrite></constraints>`,
		"wrong root":    `<other/>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseXML([]byte(in)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestDocumentFor(t *testing.T) {
	doc, err := ParseXML([]byte(xmlDocument))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, arch, pkg string
		sandbox         []Match
		processors      int
		diskMB          float64
		flags           int
	}{
		{"match", "x86_64", "kernel-default", []Match{{Value: "kvm"}}, 8, 2048, 2},
		{"match flavor strips nothing", "x86_64", "kernel-default:flavor", []Match{{Value: "chroot", Exclude: true}}, 2, 1024 * 1024, 2},
		{"short name", "x86_64", "kernel-default.openSUSE", []Match{{Value: "kvm"}}, 8, 2048, 2},
		{"other arch", "aarch64", "kernel-default", []Match{{Value: "chroot", Exclude: true}}, 2, 1024 * 1024, 2},
		{"other package", "x86_64", "bash", []Match{{Value: "chroot", Exclude: true}}, 2, 1024 * 1024, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := doc.For(tt.arch, tt.pkg)
			if !reflect.DeepEqual(c.Sandbox, tt.sandbox) || c.Hardware.Processors != tt.processors ||
				c.Hardware.DiskMB != tt.diskMB || len(c.Hardware.CPUFlags) != tt.flags {
				t.Fatalf("got %+v", c)
			}
		})
	}
	if len(doc.Constraints.Sandbox) != 1 || doc.Constraints.Sandbox[0].Value != "chroot" {
		t.Fatal("For modified the document")
	}
}

func TestMerge(t *testing.T) {
	base := Constraints{
		Sandbox:  []Match{{Value: "kvm"}},
		Linux:    Linux{Flavor: "a", Min: "1"},
		Hardware: Hardware{Processors: 2, MemoryMB: 100, CPUFlags: []Match{{Value: "x"}}},
	}
	over := Constraints{
		Sandbox:  []Match{{Value: "xen"}},
		Linux:    Linux{Min: "2"},
		Hardware: Hardware{MemoryMB: 200, DiskMB: 5, CPUFlags: []Match{{Value: "y"}}},
	}
	want := Constraints{
		Sandbox:  []Match{{Value: "kvm"}, {Value: "xen"}},
		Linux:    Linux{Flavor: "a", Min: "2"},
		Hardware: Hardware{Processors: 2, MemoryMB: 200, DiskMB: 5, CPUFlags: []Match{{Value: "x"}, {Value: "y"}}},
	}
	got := Merge(base, over)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(base.Sandbox) != 1 || len(base.Hardware.CPUFlags) != 1 {
		t.Fatal("Merge modified its input")
	}
	if !reflect.DeepEqual(Merge(Constraints{}, Constraints{}), Constraints{}) {
		t.Fatal("merging nothing must give nothing")
	}
}

func TestResolve(t *testing.T) {
	const xml = `<constraints><sandbox>kvm</sandbox><hardware><processors>4</processors></hardware></constraints>`
	tests := []struct {
		name    string
		src     Sources
		sandbox []Match
		procs   int
		memMB   float64
		wantErr bool
	}{
		{"nothing", Sources{}, nil, 0, 0, false},
		{"prjconf only", Sources{Prjconf: []string{"sandbox kvm"}}, []Match{{Value: "kvm"}}, 0, 0, false},
		{"order prjconf xml recipe", Sources{
			XML: []byte(xml), Recipe: []string{"sandbox xen", "hardware:processors 6"}, Prjconf: []string{"sandbox zvm", "hardware:processors 1", "hardware:memory:size unit=G 2"},
		}, []Match{{Value: "zvm"}, {Value: "kvm"}, {Value: "xen"}}, 6, 2048, false},
		{"package wins over prjconf", Sources{XML: []byte(xml), Prjconf: []string{"hardware:processors 1"}}, []Match{{Value: "kvm"}}, 4, 0, false},
		{"bad xml", Sources{XML: []byte("<")}, nil, 0, 0, true},
		{"bad recipe", Sources{Recipe: []string{"nope 1"}}, nil, 0, 0, true},
		{"bad prjconf", Sources{Prjconf: []string{"nope 1"}}, nil, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Resolve(tt.src)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				return
			}
			if !reflect.DeepEqual(c.Sandbox, tt.sandbox) || c.Hardware.Processors != tt.procs || c.Hardware.MemoryMB != tt.memMB {
				t.Fatalf("got %+v", c)
			}
		})
	}
}

func TestOverrideSandbox(t *testing.T) {
	base := Constraints{Sandbox: []Match{{Value: "xen"}}}
	tests := []struct {
		name, spec string
		want       []Match
	}{
		{"empty keeps", "", []Match{{Value: "xen"}}},
		{"replace", "kvm", []Match{{Value: "kvm"}}},
		{"list with exclusion", "secure !chroot", []Match{{Value: "secure"}, {Value: "chroot", Exclude: true}}},
		{"none clears", "none", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OverrideSandbox(base, tt.spec)
			if !reflect.DeepEqual(got.Sandbox, tt.want) {
				t.Fatalf("got %+v, want %+v", got.Sandbox, tt.want)
			}
		})
	}
}
