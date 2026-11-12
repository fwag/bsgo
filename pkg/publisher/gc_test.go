package publisher_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/publisher"
)

func TestExtractReferencedFiles(t *testing.T) {
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary">
    <location href="repodata/abcd1234-primary.xml.gz"/>
  </data>
  <data type="filelists">
    <location href="repodata/ef012345-filelists.xml.gz"/>
  </data>
  <data type="updateinfo">
    <location href="repodata/6789abcd-updateinfo.xml.gz"/>
  </data>
</repomd>`)

	keep, err := publisher.ExtractReferencedFiles(xmlData)
	if err != nil {
		t.Fatalf("ExtractReferencedFiles failed: %v", err)
	}

	if !keep["repomd.xml"] {
		t.Errorf("repomd.xml must be kept")
	}
	if !keep["abcd1234-primary.xml.gz"] {
		t.Errorf("expected abcd1234-primary.xml.gz to be in keep set")
	}
	if !keep["ef012345-filelists.xml.gz"] {
		t.Errorf("expected ef012345-filelists.xml.gz to be in keep set")
	}
	if !keep["6789abcd-updateinfo.xml.gz"] {
		t.Errorf("expected 6789abcd-updateinfo.xml.gz to be in keep set")
	}
	if keep["old-obsolete-primary.xml.gz"] {
		t.Errorf("obsolete file should not be in keep set")
	}
}

type GCMockS3Client struct {
	*MockS3Client
	ObjectTimes map[string]time.Time
}

func NewGCMockS3Client() *GCMockS3Client {
	return &GCMockS3Client{
		MockS3Client: NewMockS3Client(),
		ObjectTimes:  make(map[string]time.Time),
	}
}

func (g *GCMockS3Client) ListObjects(_ context.Context, prefix string) ([]publisher.S3ObjectInfo, error) {
	var res []publisher.S3ObjectInfo
	for k := range g.FilesOnS3 {
		if strings.HasPrefix(k, prefix) {
			modTime := time.Now().Add(-48 * time.Hour)
			if t, ok := g.ObjectTimes[k]; ok {
				modTime = t
			}
			res = append(res, publisher.S3ObjectInfo{
				Key:          k,
				LastModified: modTime,
			})
		}
	}
	return res, nil
}

func TestS3MetadataGarbageCollector(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	s3 := NewGCMockS3Client()
	gc := publisher.NewMetadataGarbageCollector(s3, tmpDir)

	prefix := "repos/openSUSE:Factory/standard/x86_64/repodata/"

	activeRepomd := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary">
    <location href="repodata/active-primary.xml.gz"/>
  </data>
</repomd>`)

	s3.FilesOnS3[prefix+"repomd.xml"] = activeRepomd
	s3.FilesOnS3[prefix+"active-primary.xml.gz"] = []byte("active-content")
	s3.FilesOnS3[prefix+"obsolete-old-primary.xml.gz"] = []byte("old-content")
	s3.FilesOnS3[prefix+"fresh-unreferenced-primary.xml.gz"] = []byte("fresh-content")

	// Set timestamps
	s3.ObjectTimes[prefix+"repomd.xml"] = time.Now()
	s3.ObjectTimes[prefix+"active-primary.xml.gz"] = time.Now().Add(-2 * time.Hour)
	s3.ObjectTimes[prefix+"obsolete-old-primary.xml.gz"] = time.Now().Add(-72 * time.Hour)
	s3.ObjectTimes[prefix+"fresh-unreferenced-primary.xml.gz"] = time.Now().Add(-5 * time.Minute)

	deleted, err := gc.CollectGarbage(ctx, "openSUSE:Factory", "standard", "x86_64", 24*time.Hour)
	if err != nil {
		t.Fatalf("CollectGarbage failed: %v", err)
	}

	if deleted != 1 {
		t.Errorf("expected 1 file deleted (the 72h old obsolete file), got %d", deleted)
	}

	if _, ok := s3.FilesOnS3[prefix+"obsolete-old-primary.xml.gz"]; ok {
		t.Errorf("obsolete file was NOT deleted")
	}
	if _, ok := s3.FilesOnS3[prefix+"active-primary.xml.gz"]; !ok {
		t.Errorf("active primary file was accidentally deleted")
	}
	if _, ok := s3.FilesOnS3[prefix+"fresh-unreferenced-primary.xml.gz"]; !ok {
		t.Errorf("fresh unreferenced file within grace period was accidentally deleted")
	}
	if _, ok := s3.FilesOnS3[prefix+"repomd.xml"]; !ok {
		t.Errorf("repomd.xml was accidentally deleted")
	}
}
