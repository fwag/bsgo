package signer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SignerService coordinates the signing of RPM artifacts, SLSA attestations, and repository metadata.
type SignerService struct {
	driver SignerDriver
}

func NewSignerService(driver SignerDriver) *SignerService {
	return &SignerService{
		driver: driver,
	}
}

// Driver returns the underlying SignerDriver.
func (s *SignerService) Driver() SignerDriver {
	return s.driver
}

// SignPackageArtifacts signs all RPMs in the directory and creates a detached signature for _slsa_provenance.json.
func (s *SignerService) SignPackageArtifacts(ctx context.Context, dir string, opts SignOptions) ([]string, error) {
	cleanDir := filepath.Clean(dir)
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("failed reading artifact dir %s: %w", cleanDir, err)
	}

	var signedFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Clean(filepath.Join(cleanDir, entry.Name()))

		if strings.HasSuffix(entry.Name(), ".rpm") {
			if err := s.driver.SignRPM(ctx, path, opts); err != nil {
				return nil, fmt.Errorf("failed signing RPM %s: %w", entry.Name(), err)
			}
			signedFiles = append(signedFiles, path)
			continue
		}

		if entry.Name() == "_slsa_provenance.json" {
			sigPath, err := s.signProvenanceFile(ctx, cleanDir, path, opts)
			if err != nil {
				return nil, err
			}
			signedFiles = append(signedFiles, sigPath)
		}
	}

	return signedFiles, nil
}

func (s *SignerService) signProvenanceFile(ctx context.Context, cleanDir, path string, opts SignOptions) (string, error) {
	cleanPath := filepath.Clean(path)
	// #nosec G304 -- reading sanitized provenance file path
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", err
	}
	sig, err := s.driver.SignDetached(ctx, data, opts)
	if err != nil {
		return "", fmt.Errorf("failed signing provenance: %w", err)
	}
	sigPath := filepath.Clean(filepath.Join(cleanDir, "_slsa_provenance.json.asc"))
	// #nosec G703 -- writing detached signature to validated artifact directory
	if err := os.WriteFile(sigPath, sig, 0600); err != nil {
		return "", err
	}
	return sigPath, nil
}

// SignRepositoryMetadata signs the repomd.xml file and exports the project's public key as repomd.xml.key.
func (s *SignerService) SignRepositoryMetadata(ctx context.Context, repodataDir string, opts SignOptions) (string, string, error) {
	cleanDir := filepath.Clean(repodataDir)
	repomdPath := filepath.Clean(filepath.Join(cleanDir, "repomd.xml"))

	// #nosec G304 -- reading sanitized repomd.xml path
	data, err := os.ReadFile(repomdPath)
	if err != nil {
		return "", "", fmt.Errorf("failed reading repomd.xml: %w", err)
	}

	sig, err := s.driver.SignDetached(ctx, data, opts)
	if err != nil {
		return "", "", fmt.Errorf("failed signing repomd.xml: %w", err)
	}

	sigPath := filepath.Clean(filepath.Join(cleanDir, "repomd.xml.asc"))
	// #nosec G703 -- writing detached metadata signature
	if err := os.WriteFile(sigPath, sig, 0600); err != nil {
		return "", "", fmt.Errorf("failed writing repomd.xml.asc: %w", err)
	}

	pubKey, err := s.driver.PublicKey(ctx, opts)
	if err != nil {
		return "", "", fmt.Errorf("failed exporting public key: %w", err)
	}

	keyPath := filepath.Clean(filepath.Join(cleanDir, "repomd.xml.key"))
	// #nosec G703 -- writing repository public key
	if err := os.WriteFile(keyPath, pubKey, 0600); err != nil {
		return "", "", fmt.Errorf("failed writing repomd.xml.key: %w", err)
	}

	return sigPath, keyPath, nil
}
