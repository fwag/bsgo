package signer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/openSUSE/bsgo/pkg/publisher"
)

// RepoSigner signs repository metadata for the publisher. Projects without a
// key, where key creation is disabled, are published unsigned.
type RepoSigner struct {
	signer *Signer
	// logf receives operational messages; nil discards them.
	logf     func(format string, args ...any)
	mu       sync.Mutex
	unsigned map[string]bool
}

// NewRepoSigner returns the publisher.MetadataSigner backed by s.
func NewRepoSigner(s *Signer, logf func(format string, args ...any)) *RepoSigner {
	return &RepoSigner{signer: s, logf: logf, unsigned: map[string]bool{}}
}

// SignRepositoryMetadata writes repomd.xml.asc and repomd.xml.key into a new
// directory next to repodataDir (so they are not uploaded as data files) and
// returns their paths. It returns empty paths when the project has no key.
func (r *RepoSigner) SignRepositoryMetadata(ctx context.Context, repodataDir string, opts publisher.SignOptions) (string, string, error) {
	repomd, err := readRepomd(repodataDir)
	if err != nil {
		return "", "", err
	}
	sig, err := r.signer.SignDetachedArmored(ctx, repomd, opts.Project, opts.SignFlavor)
	if errors.Is(err, ErrNoKey) {
		r.noteUnsigned(opts.Project)
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	pub, err := r.signer.PublicKeyArmored(ctx, opts.Project, opts.SignFlavor)
	if err != nil {
		return "", "", err
	}
	dir, err := os.MkdirTemp(filepath.Dir(repodataDir), "sign-")
	if err != nil {
		return "", "", fmt.Errorf("creating signature dir: %w", err)
	}
	if err := writeInDir(dir, "repomd.xml.asc", sig); err != nil {
		return "", "", err
	}
	if err := writeInDir(dir, "repomd.xml.key", pub); err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "repomd.xml.asc"), filepath.Join(dir, "repomd.xml.key"), nil
}

func readRepomd(repodataDir string) ([]byte, error) {
	root, err := os.OpenRoot(repodataDir)
	if err != nil {
		return nil, fmt.Errorf("opening repodata: %w", err)
	}
	defer root.Close()
	data, err := root.ReadFile("repomd.xml")
	if err != nil {
		return nil, fmt.Errorf("reading repomd.xml: %w", err)
	}
	return data, nil
}

func writeInDir(dir, name string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("opening %s: %w", dir, err)
	}
	defer root.Close()
	if err := root.WriteFile(name, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
}

func (r *RepoSigner) noteUnsigned(project string) {
	r.mu.Lock()
	first := !r.unsigned[project]
	r.unsigned[project] = true
	r.mu.Unlock()
	if first && r.logf != nil {
		r.logf("project %s has no signing key and none is created: publishing unsigned repositories", project)
	}
}
