package checker

import "strings"

// archCompat is the architecture path obs-build derives from a single
// architecture (extend_build_arch in /usr/lib/build/common_functions): the
// architecture followed by the ones whose binaries it can run and install.
// The first element is the target of the build.
var archCompat = map[string]string{
	"aarch64":       "aarch64:aarch64_ilp32:armv8l",
	"aarch64_ilp32": "aarch64_ilp32:aarch64:armv8l",
	"armv8hl":       "armv8hl:armv8l:armv7hl:armv7l:armv6hl:armv6l:armv5tel",
	"armv8l":        "armv8l:armv7hl:armv7l:armv6hl:armv6l:armv5tel",
	"armv7hl":       "armv7hl:armv7l:armv6hl:armv6l:armv5tel",
	"armv7l":        "armv7l:armv6l:armv5tel",
	"armv6hl":       "armv6hl:armv6l:armv5tel",
	"armv6l":        "armv6l:armv5tel",
	"mips64":        "mips64:mips",
	"i686":          "i686:i586:i486:i386",
	"i586":          "i586:i486:i386",
	"i486":          "i486:i386",
	"parisc64":      "hppa64:hppa",
	"parisc":        "hppa",
	"ppc64":         "ppc64:ppc",
	"s390x":         "s390x:s390",
	"sparc64v":      "sparc64v:sparc64:sparcv9v:sparcv9:sparcv8:sparc",
	"sparc64":       "sparc64:sparcv9:sparcv8:sparc",
	"sparcv9v":      "sparcv9v:sparcv9:sparcv8:sparc",
	"sparcv9":       "sparcv9:sparcv8:sparc",
	"sparcv8":       "sparcv8:sparc",
	"x86_64":        "x86_64:i686:i586:i486:i386",
}

// archpath is the --arch value of a build for arch: the obs-build
// compatibility path of the architecture, the configured host architecture
// when it is not part of it, and noarch.
func archpath(arch, hostArch string) string {
	path := arch
	if p, ok := archCompat[arch]; ok {
		path = p
	}
	parts := strings.Split(path, ":")
	if hostArch != "" {
		known := false
		for _, p := range parts {
			known = known || p == hostArch
		}
		if !known {
			parts = append(parts, hostArch)
		}
	}
	return strings.Join(append(parts, "noarch"), ":")
}
