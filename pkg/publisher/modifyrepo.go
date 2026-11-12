package publisher

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// ModifyRepoOptions parameters for modifyrepo_c execution.
type ModifyRepoOptions struct {
	InputFile   string
	RepodataDir string
	MDType      string
	Checksum    string
}

// ModifyRepoExecutor executes metadata modification operations on repomd.xml.
type ModifyRepoExecutor interface {
	Run(ctx context.Context, opts ModifyRepoOptions) error
}

// RealModifyRepoExecutor calls the modifyrepo_c binary.
type RealModifyRepoExecutor struct {
	BinPath string
}

// NewRealModifyRepoExecutor instantiates a real modifyrepo_c executor.
func NewRealModifyRepoExecutor() *RealModifyRepoExecutor {
	return &RealModifyRepoExecutor{
		BinPath: "modifyrepo_c",
	}
}

// Run executes modifyrepo_c with the provided options.
func (e *RealModifyRepoExecutor) Run(ctx context.Context, opts ModifyRepoOptions) error {
	args := []string{"--unique-md-filenames"}
	if opts.MDType != "" {
		args = append(args, "--mdtype="+opts.MDType)
	}
	if opts.Checksum != "" {
		args = append(args, "--checksum="+opts.Checksum)
	}
	args = append(args, opts.InputFile, opts.RepodataDir)

	// #nosec G204 -- modifyrepo_c binary path and arguments are parameterized by design
	cmd := exec.CommandContext(ctx, e.BinPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("modifyrepo_c failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// UpdateInfoXML represents the root <updates> container for RPM-MD errata.
type UpdateInfoXML struct {
	XMLName xml.Name    `xml:"updates"`
	Updates []UpdateXML `xml:"update"`
}

// UpdateXML represents an individual <update> element in updateinfo.xml.
type UpdateXML struct {
	From        string           `xml:"from,attr"`
	Status      string           `xml:"status,attr"`
	Type        string           `xml:"type,attr"`
	Version     string           `xml:"version,attr"`
	ID          string           `xml:"id"`
	Title       string           `xml:"title"`
	Severity    string           `xml:"severity,omitempty"`
	Release     string           `xml:"release,omitempty"`
	Issued      UpdateIssuedXML  `xml:"issued"`
	Description string           `xml:"description"`
	References  *UpdateRefsXML   `xml:"references,omitempty"`
	PkgList     UpdatePkgListXML `xml:"pkglist"`
}

// UpdateIssuedXML captures the issuance date attribute.
type UpdateIssuedXML struct {
	Date string `xml:"date,attr"`
}

// UpdateRefsXML wraps external advisory references (CVEs, bugs).
type UpdateRefsXML struct {
	References []UpdateRefXML `xml:"reference"`
}

// UpdateRefXML represents a single <reference> entry.
type UpdateRefXML struct {
	Href  string `xml:"href,attr"`
	ID    string `xml:"id,attr"`
	Title string `xml:"title,attr,omitempty"`
	Type  string `xml:"type,attr"`
}

// UpdatePkgListXML wraps package collection entries.
type UpdatePkgListXML struct {
	Collection UpdateCollectionXML `xml:"collection"`
}

// UpdateCollectionXML contains the affected package list.
type UpdateCollectionXML struct {
	Packages []UpdatePackageXML `xml:"package"`
}

// UpdatePackageXML defines a package affected by the update.
type UpdatePackageXML struct {
	Arch     string `xml:"arch,attr"`
	Epoch    string `xml:"epoch,attr,omitempty"`
	Name     string `xml:"name,attr"`
	Release  string `xml:"release,attr"`
	Version  string `xml:"version,attr"`
	Filename string `xml:"filename"`
}

// GenerateUpdateInfoXML generates well-formed updateinfo.xml bytes from advisories.
func GenerateUpdateInfoXML(advisories []v1.Advisory) ([]byte, error) {
	root := UpdateInfoXML{
		Updates: make([]UpdateXML, 0, len(advisories)),
	}

	for _, a := range advisories {
		root.Updates = append(root.Updates, buildUpdateXML(a))
	}

	data, err := xml.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed marshalling updateinfo xml: %w", err)
	}

	header := []byte(xml.Header)
	return append(header, data...), nil
}

func buildUpdateXML(a v1.Advisory) UpdateXML {
	status := "stable"
	uType := a.Type
	if uType == "" {
		uType = "security"
	}

	issued := a.IssuedDate
	if issued == "" {
		issued = "0"
	}

	u := UpdateXML{
		From:        "bsgo",
		Status:      status,
		Type:        uType,
		Version:     "2.0",
		ID:          a.ID,
		Title:       a.Title,
		Severity:    a.Severity,
		Release:     a.Release,
		Issued:      UpdateIssuedXML{Date: issued},
		Description: a.Description,
	}

	if len(a.References) > 0 {
		refs := make([]UpdateRefXML, 0, len(a.References))
		for _, r := range a.References {
			refs = append(refs, UpdateRefXML{
				Href:  r.Href,
				ID:    r.ID,
				Title: r.Title,
				Type:  r.Type,
			})
		}
		u.References = &UpdateRefsXML{References: refs}
	}

	var pkgs []UpdatePackageXML
	for _, p := range a.Packages {
		epoch := p.Epoch
		if epoch == "" {
			epoch = "0"
		}
		pkgs = append(pkgs, UpdatePackageXML{
			Arch:     p.Arch,
			Epoch:    epoch,
			Name:     p.Name,
			Release:  p.Release,
			Version:  p.Version,
			Filename: p.Filename,
		})
	}
	u.PkgList = UpdatePkgListXML{
		Collection: UpdateCollectionXML{Packages: pkgs},
	}

	return u
}

// EnrichMetadata injects updateinfo and appstream metadata into repodata via modifyrepo_c.
func EnrichMetadata(ctx context.Context, executor ModifyRepoExecutor, repodataDir string, req v1.PublishRequest, workDir string) error {
	if executor == nil {
		return nil
	}

	if err := injectUpdateInfo(ctx, executor, repodataDir, req, workDir); err != nil {
		return err
	}

	if err := injectAppStream(ctx, executor, repodataDir, req, workDir); err != nil {
		return err
	}

	return nil
}

func injectUpdateInfo(ctx context.Context, executor ModifyRepoExecutor, repodataDir string, req v1.PublishRequest, workDir string) error {
	if len(req.Advisories) == 0 {
		return nil
	}

	xmlData, err := GenerateUpdateInfoXML(req.Advisories)
	if err != nil {
		return err
	}

	tmpFile := filepath.Join(workDir, fmt.Sprintf("updateinfo-%s.xml", req.RequestID))
	if err := os.WriteFile(tmpFile, xmlData, 0600); err != nil {
		return fmt.Errorf("failed writing tmp updateinfo: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile) }()

	opts := ModifyRepoOptions{
		InputFile:   tmpFile,
		RepodataDir: repodataDir,
		MDType:      "updateinfo",
	}
	if err := executor.Run(ctx, opts); err != nil {
		return fmt.Errorf("failed injecting updateinfo: %w", err)
	}
	return nil
}

func injectAppStream(ctx context.Context, executor ModifyRepoExecutor, repodataDir string, req v1.PublishRequest, workDir string) error {
	var appStreamFile string

	if len(req.AppStreamData) > 0 {
		tmpFile := filepath.Join(workDir, fmt.Sprintf("appstream-%s.xml", req.RequestID))
		if err := os.WriteFile(tmpFile, req.AppStreamData, 0600); err != nil {
			return fmt.Errorf("failed writing tmp appstream: %w", err)
		}
		defer func() { _ = os.Remove(tmpFile) }()
		appStreamFile = tmpFile
	} else if req.AppStreamPath != "" && checkFileExists(req.AppStreamPath) {
		appStreamFile = req.AppStreamPath
	}

	if appStreamFile == "" {
		return nil
	}

	opts := ModifyRepoOptions{
		InputFile:   appStreamFile,
		RepodataDir: repodataDir,
		MDType:      "appstream",
	}
	if err := executor.Run(ctx, opts); err != nil {
		return fmt.Errorf("failed injecting appstream: %w", err)
	}
	return nil
}
