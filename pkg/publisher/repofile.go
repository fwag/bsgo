package publisher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// repoFileName is the name of the zypper/dnf .repo file in the repository
// root, `<project>.repo` as written by OBS.
func repoFileName(project string) string { return project + ".repo" }

// repoFileContent renders the .repo file of a published repository.
// downloadURL is the public URL of the object storage root; prefix is the S3
// prefix of the repository. Signed repositories enable gpgcheck and point at
// repomd.xml.key.
func repoFileContent(downloadURL, project, prefix string, signed bool) string {
	base := strings.TrimRight(downloadURL, "/") + "/" + prefix
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", strings.ReplaceAll(project, ":", "_"))
	fmt.Fprintf(&b, "name=%s\n", project)
	b.WriteString("type=rpm-md\n")
	fmt.Fprintf(&b, "baseurl=%s\n", base)
	if signed {
		b.WriteString("gpgcheck=1\n")
		fmt.Fprintf(&b, "gpgkey=%srepodata/repomd.xml.key\n", base)
	} else {
		b.WriteString("gpgcheck=0\n")
	}
	b.WriteString("enabled=1\n")
	return b.String()
}

// writeRepoFile writes the .repo file next to the generated repodata and
// records its path; it does nothing when no download URL is configured.
func (p *Publisher) writeRepoFile(gen *generated, project, prefix string) error {
	if p.DownloadURL == "" {
		return nil
	}
	path := filepath.Join(filepath.Dir(gen.repodataDir), repoFileName(project))
	content := repoFileContent(p.DownloadURL, project, prefix, gen.signature != "")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", repoFileName(project), err)
	}
	gen.repoFile = path
	return nil
}
