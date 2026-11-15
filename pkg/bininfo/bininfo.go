// Package bininfo describes the dependency-relevant header data of a built
// RPM. The worker emits one bininfo.json per package build result; the
// indexer turns the current set of those files into libsolv repositories.
// Binary identity is the sha256 of the RPM file, never a weaker digest.
package bininfo

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FileName is the object name of the per-package bininfo list.
const FileName = "bininfo.json"

// Dep is one dependency entry of an RPM header.
type Dep struct {
	Name string `json:"name"`
	// Op is one of "", "<", "<=", "=", ">=", ">".
	Op  string `json:"op,omitempty"`
	EVR string `json:"evr,omitempty"`
	// Pre marks a prerequisite (PreReq, Requires(pre) or Requires(post)).
	Pre bool `json:"pre,omitempty"`
}

// String renders the dependency in the "name op evr" syntax accepted by the
// libsolv dependency parser; rich dependencies are returned verbatim.
func (d Dep) String() string {
	if d.Op == "" || d.EVR == "" {
		return d.Name
	}
	return d.Name + " " + d.Op + " " + d.EVR
}

// BinInfo is the header summary of one RPM file.
type BinInfo struct {
	Name        string `json:"name"`
	Epoch       string `json:"epoch,omitempty"`
	Version     string `json:"version"`
	Release     string `json:"release"`
	Arch        string `json:"arch"`
	SourceRPM   string `json:"sourcerpm,omitempty"`
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Provides    []Dep  `json:"provides,omitempty"`
	Requires    []Dep  `json:"requires,omitempty"`
	Conflicts   []Dep  `json:"conflicts,omitempty"`
	Obsoletes   []Dep  `json:"obsoletes,omitempty"`
	Recommends  []Dep  `json:"recommends,omitempty"`
	Suggests    []Dep  `json:"suggests,omitempty"`
	Supplements []Dep  `json:"supplements,omitempty"`
	Enhances    []Dep  `json:"enhances,omitempty"`
	// Files are the packaged paths that rpm-md primary.xml lists (see
	// IsPrimaryFile); they become file provides in the index.
	Files []string `json:"files,omitempty"`
}

// IsPrimaryFile reports whether path is one of the "primary" files that
// createrepo_c puts in primary.xml and libsolv indexes with
// RPM_ADD_FILTERED_FILELIST: anything containing "bin/", anything below /etc/
// and /usr/lib/sendmail.
func IsPrimaryFile(path string) bool {
	return strings.Contains(path, "bin/") || strings.HasPrefix(path, "/etc/") || path == "/usr/lib/sendmail"
}

// EVR returns [epoch:]version-release. A missing or zero epoch is omitted,
// matching the libsolv and rpm-md convention.
func (b BinInfo) EVR() string {
	evr := b.Version + "-" + b.Release
	if b.Epoch != "" && b.Epoch != "0" {
		evr = b.Epoch + ":" + evr
	}
	return evr
}

// Marshal encodes infos as a deterministic JSON list sorted by filename.
func Marshal(infos []BinInfo) ([]byte, error) {
	sorted := append([]BinInfo{}, infos...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Filename < sorted[j].Filename })
	data, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", FileName, err)
	}
	return append(data, '\n'), nil
}

// Unmarshal decodes a bininfo.json list.
func Unmarshal(data []byte) ([]BinInfo, error) {
	var infos []BinInfo
	if err := json.Unmarshal(data, &infos); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", FileName, err)
	}
	return infos, nil
}

// SourceName returns the source package name of a sourcerpm file name
// ("foo-1.0-1.src.rpm" gives "foo"), or "" when it cannot be derived.
func SourceName(sourceRPM string) string {
	base := strings.TrimSuffix(sourceRPM, ".rpm")
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".src"), ".nosrc")
	for range 2 {
		i := strings.LastIndex(base, "-")
		if i <= 0 {
			return ""
		}
		base = base[:i]
	}
	return base
}
