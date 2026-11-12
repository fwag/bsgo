package publisher_test

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
)

func TestGenerateUpdateInfoXML(t *testing.T) {
	advisories := []v1.Advisory{
		{
			ID:          "BSGO-2026-0001",
			Title:       "Security update for curl",
			Severity:    "important",
			Type:        "security",
			Description: "curl was updated to fix critical buffer overflow vulnerabilities.",
			IssuedDate:  "1731412800",
			Release:     "1",
			References: []v1.AdvisoryRef{
				{
					Type:  "cve",
					ID:    "CVE-2026-1234",
					Href:  "https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2026-1234",
					Title: "CVE-2026-1234",
				},
				{
					Type:  "bugzilla",
					ID:    "123456",
					Href:  "https://bugzilla.suse.com/show_bug.cgi?id=123456",
					Title: "curl buffer overflow",
				},
			},
			Packages: []v1.AdvisoryPackage{
				{
					Name:     "curl",
					Epoch:    "0",
					Version:  "8.1.0",
					Release:  "1.1",
					Arch:     "x86_64",
					Filename: "curl-8.1.0-1.1.x86_64.rpm",
				},
				{
					Name:     "libcurl4",
					Epoch:    "0",
					Version:  "8.1.0",
					Release:  "1.1",
					Arch:     "x86_64",
					Filename: "libcurl4-8.1.0-1.1.x86_64.rpm",
				},
			},
		},
	}

	data, err := publisher.GenerateUpdateInfoXML(advisories)
	if err != nil {
		t.Fatalf("failed generating updateinfo XML: %v", err)
	}

	xmlStr := string(data)
	if !strings.Contains(xmlStr, "<updates>") {
		t.Errorf("missing <updates> root element")
	}
	if !strings.Contains(xmlStr, `id="BSGO-2026-0001"`) && !strings.Contains(xmlStr, `<id>BSGO-2026-0001</id>`) {
		t.Errorf("missing advisory ID BSGO-2026-0001")
	}
	if !strings.Contains(xmlStr, `CVE-2026-1234`) {
		t.Errorf("missing CVE reference")
	}
	if !strings.Contains(xmlStr, `curl-8.1.0-1.1.x86_64.rpm`) {
		t.Errorf("missing curl package in pkglist")
	}

	// Verify valid XML unmarshalling
	var parsed publisher.UpdateInfoXML
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated XML failed unmarshalling: %v", err)
	}
	if len(parsed.Updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(parsed.Updates))
	}
	u := parsed.Updates[0]
	if u.ID != "BSGO-2026-0001" || u.Severity != "important" {
		t.Errorf("unexpected update data: ID=%s, Severity=%s", u.ID, u.Severity)
	}
	if len(u.PkgList.Collection.Packages) != 2 {
		t.Errorf("expected 2 packages in collection, got %d", len(u.PkgList.Collection.Packages))
	}
}

func TestRealModifyRepoExecution(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	repoDir := filepath.Join(tmpDir, "repo")
	_ = os.MkdirAll(repoDir, 0755)

	// 1. Initialize repo with createrepo_c
	cr := publisher.NewRealCreateRepoExecutor()
	if err := cr.Run(ctx, publisher.CreateRepoOptions{Dir: repoDir}); err != nil {
		t.Fatalf("createrepo_c failed: %v", err)
	}

	repodataDir := filepath.Join(repoDir, "repodata")
	mr := publisher.NewRealModifyRepoExecutor()

	// 2. Generate and inject updateinfo.xml
	advisories := []v1.Advisory{
		{
			ID:          "BSGO-2026-TEST",
			Title:       "Test Advisory",
			Severity:    "low",
			Type:        "bugfix",
			Description: "Test advisory injection",
			IssuedDate:  "1731412800",
			Packages: []v1.AdvisoryPackage{
				{Name: "test-pkg", Version: "1.0", Release: "1", Arch: "x86_64", Filename: "test-pkg-1.0-1.x86_64.rpm"},
			},
		},
	}
	updateinfoData, err := publisher.GenerateUpdateInfoXML(advisories)
	if err != nil {
		t.Fatalf("failed generating updateinfo: %v", err)
	}
	updateinfoFile := filepath.Join(tmpDir, "updateinfo.xml")
	if err := os.WriteFile(updateinfoFile, updateinfoData, 0644); err != nil {
		t.Fatalf("failed writing updateinfo file: %v", err)
	}

	if err := mr.Run(ctx, publisher.ModifyRepoOptions{
		InputFile:   updateinfoFile,
		RepodataDir: repodataDir,
		MDType:      "updateinfo",
	}); err != nil {
		t.Fatalf("modifyrepo_c updateinfo failed: %v", err)
	}

	// 3. Inject mock appstream.xml
	appstreamData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<components origin="bsgo" version="0.14">
  <component type="desktop-application">
    <id>org.opensuse.bsgo</id>
    <name>BSGO</name>
    <summary>Build Service Go</summary>
  </component>
</components>`)
	appstreamFile := filepath.Join(tmpDir, "appstream.xml")
	if err := os.WriteFile(appstreamFile, appstreamData, 0644); err != nil {
		t.Fatalf("failed writing appstream file: %v", err)
	}

	if err := mr.Run(ctx, publisher.ModifyRepoOptions{
		InputFile:   appstreamFile,
		RepodataDir: repodataDir,
		MDType:      "appstream",
	}); err != nil {
		t.Fatalf("modifyrepo_c appstream failed: %v", err)
	}

	// 4. Verify repomd.xml contains both updateinfo and appstream data sections
	repomdContent, err := os.ReadFile(filepath.Join(repodataDir, "repomd.xml"))
	if err != nil {
		t.Fatalf("failed reading repomd.xml: %v", err)
	}
	repomdStr := string(repomdContent)
	if !strings.Contains(repomdStr, `type="updateinfo"`) {
		t.Errorf("repomd.xml does not contain type=\"updateinfo\"")
	}
	if !strings.Contains(repomdStr, `type="appstream"`) {
		t.Errorf("repomd.xml does not contain type=\"appstream\"")
	}
}

type MockModifyRepoExecutor struct {
	Invocations []publisher.ModifyRepoOptions
}

func (m *MockModifyRepoExecutor) Run(_ context.Context, opts publisher.ModifyRepoOptions) error {
	m.Invocations = append(m.Invocations, opts)
	// Mock creating the injected metadata file in repodata
	filename := filepath.Join(opts.RepodataDir, "mock-hash-"+opts.MDType+".xml.gz")
	return os.WriteFile(filename, []byte("mock-data-"+opts.MDType), 0644)
}

func TestPublisherMetadataEnrichmentWorkflow(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cr := &MockCreateRepoExecutor{}
	mr := &MockModifyRepoExecutor{}
	s3 := NewMockS3Client()
	lockMgr := publisher.NewLocalLockManager()

	pub := publisher.NewPublisher(cr, s3, lockMgr, tmpDir)
	pub.SetModifyRepoExecutor(mr)

	req := publisher.PublishRequest{
		RequestID:    "pub-enrich-01",
		Project:      "openSUSE:Factory",
		Target:       "standard",
		Architecture: "x86_64",
		Advisories: []v1.Advisory{
			{
				ID:       "SUSE-2026-9999",
				Title:    "Critical fix",
				Severity: "critical",
				Type:     "security",
				Packages: []v1.AdvisoryPackage{
					{Name: "bash", Version: "5.2", Release: "1", Arch: "x86_64", Filename: "bash-5.2-1.x86_64.rpm"},
				},
			},
		},
		AppStreamData: []byte("<components origin='bsgo'/>"),
	}

	res, err := pub.ProcessPublishRequest(ctx, req)
	if err != nil {
		t.Fatalf("ProcessPublishRequest failed: %v", err)
	}

	if len(mr.Invocations) != 2 {
		t.Fatalf("expected 2 modifyrepo invocations (updateinfo, appstream), got %d", len(mr.Invocations))
	}
	if mr.Invocations[0].MDType != "updateinfo" {
		t.Errorf("expected first invocation to be updateinfo, got %s", mr.Invocations[0].MDType)
	}
	if mr.Invocations[1].MDType != "appstream" {
		t.Errorf("expected second invocation to be appstream, got %s", mr.Invocations[1].MDType)
	}

	// Verify enriched files are uploaded to S3
	hasUpdateInfo := false
	hasAppStream := false
	for _, k := range res.UploadedXMLKeys {
		if strings.Contains(k, "updateinfo") {
			hasUpdateInfo = true
		}
		if strings.Contains(k, "appstream") {
			hasAppStream = true
		}
	}
	if !hasUpdateInfo {
		t.Errorf("expected updateinfo file to be uploaded to S3")
	}
	if !hasAppStream {
		t.Errorf("expected appstream file to be uploaded to S3")
	}
}
