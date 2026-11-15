package publisher

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SubRepoType represents the sub-repository partition classification (standard, debug, or source).
type SubRepoType string

const (
	// SubRepoStandard represents the runtime binary RPM repository (e.g. repos/<prj>/<target>/<arch>/).
	SubRepoStandard SubRepoType = "standard"
	// SubRepoDebug represents the debuginfo and debugsource RPM repository (e.g. repos/<prj>/<target>/debug/<arch>/).
	SubRepoDebug SubRepoType = "debug"
	// SubRepoSource represents the source RPM repository (e.g. repos/<prj>/<target>/src/).
	SubRepoSource SubRepoType = "src"
)

// ClassifyRPM categorizes an RPM filename or path into standard, debug, or source sub-repository.
func ClassifyRPM(filename string) SubRepoType {
	base := filepath.Base(filename)
	if idx := strings.Index(base, "?"); idx != -1 {
		base = base[:idx]
	}
	base = strings.ToLower(base)

	if isSourceRPM(base) {
		return SubRepoSource
	}
	if isDebugRPM(base) {
		return SubRepoDebug
	}
	return SubRepoStandard
}

func isSourceRPM(filename string) bool {
	return strings.HasSuffix(filename, ".src.rpm") || strings.HasSuffix(filename, ".nosrc.rpm")
}

func isDebugRPM(filename string) bool {
	if strings.HasSuffix(filename, "-debuginfo.rpm") || strings.HasSuffix(filename, "-debugsource.rpm") {
		return true
	}
	return strings.Contains(filename, "-debuginfo-") || strings.Contains(filename, "-debugsource-")
}

// SubRepoS3Prefix returns the canonical S3 prefix for a specific sub-repository.
func SubRepoS3Prefix(project, target, arch string, sr SubRepoType) string {
	switch sr {
	case SubRepoDebug:
		return fmt.Sprintf("repos/%s/%s/debug/%s/", project, target, arch)
	case SubRepoSource:
		return fmt.Sprintf("repos/%s/%s/src/", project, target)
	default:
		return fmt.Sprintf("repos/%s/%s/%s/", project, target, arch)
	}
}
