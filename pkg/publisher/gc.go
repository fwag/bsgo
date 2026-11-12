package publisher

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RepomdManifest represents the root <repomd> element in repomd.xml.
type RepomdManifest struct {
	XMLName xml.Name         `xml:"repomd"`
	Data    []RepomdDataNode `xml:"data"`
}

// RepomdDataNode represents an individual <data> entry in repomd.xml.
type RepomdDataNode struct {
	Type     string         `xml:"type,attr"`
	Location RepomdLocation `xml:"location"`
}

// RepomdLocation captures the href attribute of a metadata entry.
type RepomdLocation struct {
	Href string `xml:"href,attr"`
}

// MetadataGarbageCollector removes superseded historical repodata files from S3.
type MetadataGarbageCollector struct {
	s3Client S3Client
	workDir  string
}

// NewMetadataGarbageCollector creates a new S3 metadata cleaner.
func NewMetadataGarbageCollector(s3 S3Client, workDir string) *MetadataGarbageCollector {
	return &MetadataGarbageCollector{
		s3Client: s3,
		workDir:  workDir,
	}
}

// CollectGarbage removes unreferenced hash-prefixed XML files older than maxAge for all subrepos.
func (g *MetadataGarbageCollector) CollectGarbage(ctx context.Context, project, target, arch string, maxAge time.Duration) (int, error) {
	if g.s3Client == nil {
		return 0, nil
	}

	subRepos := []SubRepoType{SubRepoStandard, SubRepoDebug, SubRepoSource}
	totalDeleted := 0

	for _, sr := range subRepos {
		deleted, err := g.cleanSubRepoMetadata(ctx, project, target, arch, sr, maxAge)
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
	}

	return totalDeleted, nil
}

func (g *MetadataGarbageCollector) cleanSubRepoMetadata(ctx context.Context, project, target, arch string, sr SubRepoType, maxAge time.Duration) (int, error) {
	s3Prefix := SubRepoS3Prefix(project, target, arch, sr)
	repodataPrefix := s3Prefix + "repodata/"

	objects, err := g.s3Client.ListObjects(ctx, repodataPrefix)
	if err != nil || len(objects) == 0 {
		return 0, err
	}

	tmpMetaDir := filepath.Join(g.workDir, "gc", project, target, string(sr))
	_ = os.MkdirAll(tmpMetaDir, 0750)
	defer func() { _ = os.RemoveAll(tmpMetaDir) }()

	if err := g.s3Client.DownloadMetadata(ctx, repodataPrefix, tmpMetaDir); err != nil {
		return 0, nil // Nothing downloaded or error accessing metadata
	}

	repomdPath := filepath.Join(tmpMetaDir, "repomd.xml")
	data, err := os.ReadFile(repomdPath)
	if err != nil {
		return 0, nil // No active repomd.xml found, do not GC
	}

	keepSet, err := ExtractReferencedFiles(data)
	if err != nil {
		return 0, fmt.Errorf("failed parsing active repomd.xml: %w", err)
	}

	deletedCount := 0
	now := time.Now()

	for _, obj := range objects {
		baseName := filepath.Base(obj.Key)
		if keepSet[baseName] {
			continue
		}
		if maxAge > 0 && now.Sub(obj.LastModified) < maxAge {
			continue
		}

		if err := g.s3Client.DeleteObject(ctx, obj.Key); err == nil {
			deletedCount++
		}
	}

	return deletedCount, nil
}

// ExtractReferencedFiles parses repomd.xml and extracts all currently active metadata files.
func ExtractReferencedFiles(xmlData []byte) (map[string]bool, error) {
	var manifest RepomdManifest
	if err := xml.Unmarshal(xmlData, &manifest); err != nil {
		return nil, err
	}

	keep := map[string]bool{
		"repomd.xml":     true,
		"repomd.xml.asc": true,
		"repomd.xml.key": true,
	}

	for _, d := range manifest.Data {
		if d.Location.Href != "" {
			keep[filepath.Base(d.Location.Href)] = true
		}
	}

	return keep, nil
}
