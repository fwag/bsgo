package publisher

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// reusableTypes are the metadata types createrepo_c --update merges.
var reusableTypes = map[string]bool{"primary": true, "filelists": true, "other": true}

// Listing is a published repository as seen in object storage.
type Listing struct {
	// Objects maps the base name of every object directly below the
	// repository prefix to its size.
	Objects map[string]int64
	// HasRepomd reports whether repodata/repomd.xml exists.
	HasRepomd bool
}

// listRepo lists the objects of a published sub-repository.
func listRepo(ctx context.Context, store ObjectStore, prefix string) (*Listing, error) {
	objs, err := store.ListObjects(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", prefix, err)
	}
	l := &Listing{Objects: map[string]int64{}}
	for _, o := range objs {
		rel := strings.TrimPrefix(o.Key, prefix)
		if rel == "repodata/repomd.xml" {
			l.HasRepomd = true
		}
		if rel != "" && !strings.Contains(rel, "/") {
			l.Objects[rel] = o.Size
		}
	}
	return l, nil
}

// packageEntry is one package of a primary.xml.
type packageEntry struct {
	Href     string
	Checksum string
	CsumType string
}

// parsePrimary reads the packages of a gzip compressed primary.xml.
func parsePrimary(r io.Reader) ([]packageEntry, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("primary.xml is not gzip compressed: %w", err)
	}
	defer zr.Close()
	type xmlPkg struct {
		Checksum struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"checksum"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	}
	dec := xml.NewDecoder(zr)
	var out []packageEntry
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("parsing primary.xml: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "package" {
			continue
		}
		var p xmlPkg
		if err := dec.DecodeElement(&p, &start); err != nil {
			return nil, fmt.Errorf("parsing primary.xml package: %w", err)
		}
		out = append(out, packageEntry{
			Href: p.Location.Href, Checksum: strings.TrimSpace(p.Checksum.Value), CsumType: p.Checksum.Type,
		})
	}
}

// repomdLocations returns type -> href of the data entries of a repomd.xml.
func repomdLocations(data []byte) (map[string]string, error) {
	var m RepomdManifest
	if err := xml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing repomd.xml: %w", err)
	}
	out := map[string]string{}
	for _, d := range m.Data {
		out[d.Type] = d.Location.Href
	}
	return out, nil
}

// oldRepodata is the previously published metadata of a sub-repository,
// materialized in the publish temp dir. It is the cache that makes metadata
// generation incremental: it lives in object storage as the published
// repodata itself and is never kept on local disk across publishes.
type oldRepodata struct {
	// Dir contains repodata/ and can be given to createrepo_c --update-md-path.
	Dir string
	// Sums maps package file name to its sha256.
	Sums map[string]string
}

// usable reports whether previous metadata was found.
func (o oldRepodata) usable() bool { return o.Dir != "" }

// fetchOldRepodata downloads the primary, filelists and other files of the
// published repomd.xml into dir/repodata. It returns the zero value when there is no
// usable previous metadata (none published, or not gzip compressed); the
// caller then regenerates everything from the RPM files.
func fetchOldRepodata(ctx context.Context, store ObjectStore, prefix, dir string, l *Listing) (oldRepodata, error) {
	none := oldRepodata{}
	if !l.HasRepomd {
		return none, nil
	}
	raw, err := store.ReadObject(ctx, prefix+"repodata/repomd.xml")
	if err != nil {
		return none, fmt.Errorf("reading published repomd.xml: %w", err)
	}
	locs, err := repomdLocations(raw)
	if err != nil {
		return none, nil // unreadable metadata: regenerate
	}
	if !strings.HasSuffix(locs["primary"], ".gz") {
		return none, nil
	}
	rdir := filepath.Join(dir, "repodata")
	if err := os.MkdirAll(rdir, 0o750); err != nil {
		return none, fmt.Errorf("creating old repodata dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "repomd.xml"), raw, 0o600); err != nil {
		return none, fmt.Errorf("writing old repomd.xml: %w", err)
	}
	for typ, href := range locs {
		if !reusableTypes[typ] || href == "" {
			continue
		}
		name := path.Base(href)
		if err := store.DownloadFile(ctx, prefix+"repodata/"+name, filepath.Join(rdir, name)); err != nil {
			return none, fmt.Errorf("downloading published %s metadata: %w", typ, err)
		}
	}
	sums, err := primarySums(filepath.Join(rdir, path.Base(locs["primary"])))
	if err != nil {
		return none, nil // corrupt cache: regenerate
	}
	return oldRepodata{Dir: dir, Sums: sums}, nil
}

// primarySums maps file name to sha256 for a primary.xml.gz on disk.
func primarySums(file string) (map[string]string, error) {
	// #nosec G304 -- file is inside the publish temp dir
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pkgs, err := parsePrimary(f)
	if err != nil {
		return nil, err
	}
	sums := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		if p.CsumType == "sha256" {
			sums[path.Base(p.Href)] = p.Checksum
		}
	}
	return sums, nil
}

// verifyRepodata checks the generated repository: it must list exactly the
// wanted packages, each with the expected sha256.
func verifyRepodata(repodataDir string, want map[string]string) error {
	raw, err := os.ReadFile(filepath.Join(filepath.Clean(repodataDir), "repomd.xml"))
	if err != nil {
		return fmt.Errorf("reading generated repomd.xml: %w", err)
	}
	locs, err := repomdLocations(raw)
	if err != nil {
		return err
	}
	if locs["primary"] == "" {
		return fmt.Errorf("generated repomd.xml has no primary metadata")
	}
	got, err := primarySums(filepath.Join(repodataDir, path.Base(locs["primary"])))
	if err != nil {
		return fmt.Errorf("reading generated primary.xml: %w", err)
	}
	for name, sha := range want {
		if got[name] != sha {
			return fmt.Errorf("generated metadata of %s has checksum %q, want sha256 %s", name, got[name], sha)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("generated metadata lists %d packages, want %d", len(got), len(want))
	}
	return nil
}
