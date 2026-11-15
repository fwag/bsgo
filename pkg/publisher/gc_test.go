package publisher_test

import (
	"context"
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

func TestS3MetadataGarbageCollector(t *testing.T) {
	ctx := context.Background()

	s3 := publisher.NewMemoryStore()
	gc := publisher.NewMetadataGarbageCollector(s3)

	prefix := "repos/openSUSE:Factory/standard/x86_64/repodata/"

	activeRepomd := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary">
    <location href="repodata/active-primary.xml.gz"/>
  </data>
</repomd>`)

	s3.Set(prefix+"repomd.xml", activeRepomd)
	s3.Set(prefix+"active-primary.xml.gz", []byte("active-content"))
	s3.Set(prefix+"obsolete-old-primary.xml.gz", []byte("old-content"))
	s3.Set(prefix+"fresh-unreferenced-primary.xml.gz", []byte("fresh-content"))

	// Set timestamps
	s3.SetModTime(prefix+"repomd.xml", time.Now())
	s3.SetModTime(prefix+"active-primary.xml.gz", time.Now().Add(-2*time.Hour))
	s3.SetModTime(prefix+"obsolete-old-primary.xml.gz", time.Now().Add(-72*time.Hour))
	s3.SetModTime(prefix+"fresh-unreferenced-primary.xml.gz", time.Now().Add(-5*time.Minute))

	deleted, err := gc.CollectGarbage(ctx, "openSUSE:Factory", "standard", "x86_64", 24*time.Hour)
	if err != nil {
		t.Fatalf("CollectGarbage failed: %v", err)
	}

	if deleted != 1 {
		t.Errorf("expected 1 file deleted (the 72h old obsolete file), got %d", deleted)
	}

	if s3.Has(prefix + "obsolete-old-primary.xml.gz") {
		t.Errorf("obsolete file was NOT deleted")
	}
	if !s3.Has(prefix + "active-primary.xml.gz") {
		t.Errorf("active primary file was accidentally deleted")
	}
	if !s3.Has(prefix + "fresh-unreferenced-primary.xml.gz") {
		t.Errorf("fresh unreferenced file within grace period was accidentally deleted")
	}
	if !s3.Has(prefix + "repomd.xml") {
		t.Errorf("repomd.xml was accidentally deleted")
	}
}
