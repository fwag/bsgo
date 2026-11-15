package publisher

import (
	"context"
	"encoding/xml"
	"fmt"
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
	store ObjectStore
}

// NewMetadataGarbageCollector creates a new S3 metadata cleaner.
func NewMetadataGarbageCollector(store ObjectStore) *MetadataGarbageCollector {
	return &MetadataGarbageCollector{store: store}
}

// CollectGarbage removes unreferenced hash-prefixed XML files older than maxAge for all subrepos.
func (g *MetadataGarbageCollector) CollectGarbage(ctx context.Context, project, target, arch string, maxAge time.Duration) (int, error) {
	if g.store == nil {
		return 0, nil
	}
	totalDeleted := 0
	for _, sr := range []SubRepoType{SubRepoStandard, SubRepoDebug, SubRepoSource} {
		deleted, err := g.cleanSubRepoMetadata(ctx, project, target, arch, sr, maxAge)
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
	}
	return totalDeleted, nil
}

func (g *MetadataGarbageCollector) cleanSubRepoMetadata(ctx context.Context, project, target, arch string, sr SubRepoType, maxAge time.Duration) (int, error) {
	repodataPrefix := SubRepoS3Prefix(project, target, arch, sr) + "repodata/"
	objects, err := g.store.ListObjects(ctx, repodataPrefix)
	if err != nil {
		return 0, fmt.Errorf("listing %s: %w", repodataPrefix, err)
	}
	hasRepomd := false
	for _, obj := range objects {
		hasRepomd = hasRepomd || obj.Key == repodataPrefix+"repomd.xml"
	}
	if !hasRepomd {
		return 0, nil // no active repomd.xml found, do not GC
	}
	data, err := g.store.ReadObject(ctx, repodataPrefix+"repomd.xml")
	if err != nil {
		return 0, fmt.Errorf("reading active repomd.xml: %w", err)
	}
	keepSet, err := ExtractReferencedFiles(data)
	if err != nil {
		return 0, fmt.Errorf("failed parsing active repomd.xml: %w", err)
	}

	deletedCount := 0
	now := time.Now()
	for _, obj := range objects {
		if keepSet[filepath.Base(obj.Key)] || (maxAge > 0 && now.Sub(obj.LastModified) < maxAge) {
			continue
		}
		if err := g.store.DeleteObject(ctx, obj.Key); err != nil {
			return deletedCount, fmt.Errorf("deleting %s: %w", obj.Key, err)
		}
		deletedCount++
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
