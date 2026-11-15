package worker

import (
	"strings"
)

// cando maps the host architecture of a worker to the build architectures it
// can build; an entry "arch:helper" needs the personality helper (linux32,
// linux64) in front of the build command. It is the table of OBS
// (src/backend/BSCando.pm, build/PBuild/Cando.pm).
var cando = map[string][]string{
	"aarch64":       {"aarch64", "aarch64_ilp32", "armv8l:linux32", "armv7l:linux32", "armv7hl:linux32", "armv6l:linux32", "armv6hl:linux32"},
	"aarch64_ilp32": {"aarch64_ilp32", "aarch64"},
	"armv4l":        {"armv4l"},
	"armv5l":        {"armv4l", "armv5l", "armv5el"},
	"armv6l":        {"armv4l", "armv5l", "armv6l", "armv5el", "armv6el"},
	"armv7l":        {"armv4l", "armv5l", "armv6l", "armv7l", "armv5el", "armv6el", "armv6hl", "armv7el", "armv7hl"},
	"armv8l":        {"armv4l", "armv5l", "armv6l", "armv7l", "armv5el", "armv6el", "armv6hl", "armv7el", "armv7hl", "armv8el", "armv8hl", "armv8l"},
	"sh4":           {"sh4"},
	"i586":          {"i586"},
	"i686":          {"i586", "i686"},
	"x86_64":        {"x86_64", "i586:linux32", "i686:linux32"},
	"k1om":          {"k1om"},
	"parisc":        {"hppa", "hppa64:linux64"},
	"parisc64":      {"hppa64", "hppa:linux32"},
	"ppc":           {"ppc"},
	"ppc64":         {"ppc64le", "ppc64", "ppc:linux32"},
	"ppc64p7":       {"ppc64le", "ppc64p7", "ppc:linux32"},
	"ppc64le":       {"ppc64le", "ppc64", "ppc:linux32"},
	"ia64":          {"ia64"},
	"loongarch64":   {"loongarch64"},
	"riscv64":       {"riscv64"},
	"s390":          {"s390"},
	"s390x":         {"s390x", "s390:linux32"},
	"sparc":         {"sparcv8", "sparc"},
	"sparc64":       {"sparc64v", "sparc64", "sparcv9v", "sparcv9", "sparcv8:linux32", "sparc:linux32"},
	"mips":          {"mips"},
	"mips64":        {"mips64", "mips:mips32"},
	"m68k":          {"m68k"},
}

// DefaultArchs returns the architectures a worker of host architecture host
// can build in table order; an unknown host builds only itself.
func DefaultArchs(host string) []string {
	entries, ok := cando[host]
	if !ok {
		return []string{host}
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		arch, _, _ := strings.Cut(e, ":")
		out = append(out, arch)
	}
	return out
}

// PersonalityHelper returns the command (for example "linux32") that must
// prefix the build of arch on a host of architecture host, or "" when none is
// needed. obs-build does not switch the personality itself: bs_worker
// prepends the helper, and so does the bsgo worker.
func PersonalityHelper(host, arch string) string {
	for _, e := range cando[host] {
		if a, helper, ok := strings.Cut(e, ":"); ok && a == arch {
			return helper
		}
	}
	return ""
}
