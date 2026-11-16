package signing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Objects is the object storage of artifacts; storage/s3.S3Storage satisfies it.
type Objects interface {
	DownloadFile(ctx context.Context, key, localPath string) error
	UploadFile(ctx context.Context, localPath, key string) error
}

// Keys resolves the key that signs a project; *signer.Signer satisfies it.
type Keys interface {
	ResolveKey(ctx context.Context, project, flavor string) (*storage.ProjectSigningKey, error)
}

// States reads the build state; storage.Store satisfies it.
type States interface {
	GetBuildState(ctx context.Context, project, repo, arch, pkg, flavor string) (*storage.PackageBuildState, error)
}

// Defaults of the retry policy.
const (
	DefaultAttempts = 4
	DefaultBackoff  = 2 * time.Second
)

// Stage signs the RPMs of finished jobs. It keeps no state between jobs, so
// any number of replicas can run it.
type Stage struct {
	Keys    Keys
	Objects Objects
	States  States
	Tool    Tool
	// Publish sends the encoded answer (job-signed message).
	Publish func(ctx context.Context, data []byte) error
	// TempRoot is the parent of the per-job working directories.
	TempRoot string
	// Attempts is the number of tries before a job fails (default 4); the
	// wait between tries doubles from Backoff (default 2s).
	Attempts int
	Backoff  time.Duration
	// Sleep waits between tries; nil means a context-aware time.Sleep.
	Sleep func(ctx context.Context, d time.Duration)
}

// pending reports whether the job still waits for its signatures: its unit is
// in state signing with the same job ID. Answered, superseded and unknown jobs
// are not pending, which makes redelivery harmless.
func (s *Stage) pending(ctx context.Context, j Job) (bool, error) {
	pkg, flavor := buildrepo.SplitUnit(j.Package)
	cur, err := s.States.GetBuildState(ctx, j.Project, j.Target, j.Architecture, pkg, flavor)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read build state of job %s: %w", j.JobID, err)
	}
	return cur.JobID == j.JobID && cur.State == storage.StateSigning, nil
}

// Handle processes one job-finished message and publishes the answer. A job
// that is not in state signing any more is skipped.
func (s *Stage) Handle(ctx context.Context, data []byte) error {
	j, err := Decode(data)
	if err != nil {
		return err
	}
	if ok, err := s.pending(ctx, j); err != nil || !ok {
		return err
	}
	answer, err := Encode(s.Process(ctx, j))
	if err != nil {
		return err
	}
	return s.Publish(ctx, answer)
}

// Fail publishes a terminal failure answer for a request that could not be
// processed (for example after its last delivery), so the unit does not stay
// in state signing.
func (s *Stage) Fail(ctx context.Context, data []byte, cause error) error {
	j, err := Decode(data)
	if err != nil {
		return err
	}
	if ok, err := s.pending(ctx, j); err != nil || !ok {
		return err
	}
	j.Error = cause.Error()
	answer, err := Encode(j)
	if err != nil {
		return err
	}
	return s.Publish(ctx, answer)
}

// Process signs the job with retries and returns the answer: the job with the
// signed artifacts, or with Error set when every attempt failed.
func (s *Stage) Process(ctx context.Context, j Job) Job {
	attempts, backoff := s.Attempts, s.Backoff
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	var err error
	for i := 0; i < attempts; i++ {
		var arts []indexer.Artifact
		if arts, err = s.Sign(ctx, j); err == nil {
			j.Artifacts, j.Error = arts, ""
			return j
		}
		if ctx.Err() != nil || i == attempts-1 {
			break
		}
		s.sleep(ctx, backoff<<i)
	}
	j.Error = err.Error()
	return j
}

func (s *Stage) sleep(ctx context.Context, d time.Duration) {
	if s.Sleep != nil {
		s.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func isRPM(name string) bool { return strings.HasSuffix(name, ".rpm") }

// safeName rejects artifact names that would escape the working directory.
func safeName(name string) error {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return fmt.Errorf("unsafe artifact name %q", name)
	}
	return nil
}

// Sign is one attempt: download, sign, verify, re-index, upload. It returns
// the artifact list after signing. Every step is safe to repeat: RPMs that
// already verify against the project key (a previous attempt uploaded them)
// are kept as they are, and signing is deterministic.
func (s *Stage) Sign(ctx context.Context, j Job) ([]indexer.Artifact, error) {
	key, err := s.Keys.ResolveKey(ctx, j.Project, signer.DefaultFlavor)
	if err != nil {
		return nil, fmt.Errorf("resolving signing key of %s: %w", j.Project, err)
	}
	if s.TempRoot != "" {
		if err := os.MkdirAll(s.TempRoot, 0o750); err != nil {
			return nil, err
		}
	}
	dir, err := os.MkdirTemp(s.TempRoot, "bsgo-signing-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	rpms, err := s.fetch(ctx, dir, j)
	if err != nil {
		return nil, err
	}
	created, err := keyCreated(key.PublicKeyArmored)
	if err != nil {
		return nil, fmt.Errorf("signing key of %s: %w", j.Project, err)
	}
	ref := KeyRef{
		Project: key.ProjectName, Flavor: key.KeyFlavor, Fingerprint: key.Fingerprint,
		EncryptedKey: key.EncryptedPrivateKey, Created: created,
	}
	if err := s.signAll(ctx, key.PublicKeyArmored, ref, rpms); err != nil {
		return nil, err
	}
	return s.publish(ctx, dir, j, rpms)
}

// keyCreated reads the creation time of an armored OpenPGP public key.
func keyCreated(armored string) (time.Time, error) {
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil {
		return time.Time{}, fmt.Errorf("reading public key: %w", err)
	}
	if len(ring) != 1 {
		return time.Time{}, fmt.Errorf("public key block holds %d keys, want 1", len(ring))
	}
	return ring[0].PrimaryKey.CreationTime, nil
}

// localRPM is a downloaded RPM and the artifact it belongs to.
type localRPM struct {
	art    indexer.Artifact
	path   string
	signed bool // already carries a valid project signature
}

// fetch downloads the job's RPMs and verifies the worker's sha256. A file
// with another sha256 is accepted only when it already carries a valid
// project signature (left by an earlier attempt).
func (s *Stage) fetch(ctx context.Context, dir string, j Job) ([]*localRPM, error) {
	var rpms []*localRPM
	for _, a := range j.Artifacts {
		if !isRPM(a.Filename) {
			continue
		}
		if err := safeName(a.Filename); err != nil {
			return nil, err
		}
		l := &localRPM{art: a, path: filepath.Join(dir, a.Filename)}
		if err := s.Objects.DownloadFile(ctx, a.S3Key, l.path); err != nil {
			return nil, fmt.Errorf("downloading %s: %w", a.S3Key, err)
		}
		sum, err := fileSHA256(l.path)
		if err != nil {
			return nil, err
		}
		l.signed = sum != a.SHA256
		rpms = append(rpms, l)
	}
	return rpms, nil
}

// signAll signs the RPMs that are not signed yet and verifies all of them.
// A file whose sha256 differs from the worker's must already verify, else
// the download was corrupted or replaced.
func (s *Stage) signAll(ctx context.Context, pub string, ref KeyRef, rpms []*localRPM) error {
	var all []string
	for _, l := range rpms {
		all = append(all, l.path)
		if l.signed {
			if err := s.Tool.Verify(ctx, pub, []string{l.path}); err != nil {
				return fmt.Errorf("%s does not match the sha256 reported by the worker and is not signed by the project key: %w",
					l.art.Filename, err)
			}
			continue
		}
		if err := s.Tool.Sign(ctx, l.path, ref); err != nil {
			return err
		}
	}
	if err := s.Tool.Verify(ctx, pub, all); err != nil {
		return fmt.Errorf("verifying signed RPMs: %w", err)
	}
	return nil
}

// publish re-extracts the binary index of the signed RPMs (their sha256
// changed, the header data did not), uploads RPMs and bininfo.json over the
// unsigned objects and returns the updated artifact list.
func (s *Stage) publish(ctx context.Context, dir string, j Job, rpms []*localRPM) ([]indexer.Artifact, error) {
	paths := make([]string, len(rpms))
	for i, l := range rpms {
		paths[i] = l.path
	}
	infos, err := bininfo.ExtractAll(ctx, paths)
	if err != nil {
		return nil, fmt.Errorf("extracting bininfo of signed RPMs: %w", err)
	}
	raw, err := bininfo.Marshal(infos)
	if err != nil {
		return nil, err
	}
	bi := filepath.Join(dir, bininfo.FileName)
	if err := os.WriteFile(bi, raw, 0o600); err != nil {
		return nil, err
	}
	updated := map[string]indexer.Artifact{}
	for _, l := range rpms {
		a, err := s.upload(ctx, l.path, l.art.S3Key, l.art.Filename)
		if err != nil {
			return nil, err
		}
		updated[a.Filename] = a
	}
	biKey := bininfoKey(j)
	a, err := s.upload(ctx, bi, biKey, bininfo.FileName)
	if err != nil {
		return nil, err
	}
	updated[a.Filename] = a
	return mergeArtifacts(j.Artifacts, updated), nil
}

// bininfoKey is the object key of the job's bininfo.json: the one the worker
// reported, else next to the first RPM.
func bininfoKey(j Job) string {
	var dir string
	for _, a := range j.Artifacts {
		if a.Filename == bininfo.FileName {
			return a.S3Key
		}
		if isRPM(a.Filename) && dir == "" {
			dir = path.Dir(a.S3Key)
		}
	}
	return path.Join(dir, bininfo.FileName)
}

func (s *Stage) upload(ctx context.Context, local, key, name string) (indexer.Artifact, error) {
	if err := s.Objects.UploadFile(ctx, local, key); err != nil {
		return indexer.Artifact{}, fmt.Errorf("uploading %s: %w", key, err)
	}
	sum, err := fileSHA256(local)
	if err != nil {
		return indexer.Artifact{}, err
	}
	st, err := os.Stat(local)
	if err != nil {
		return indexer.Artifact{}, err
	}
	return indexer.Artifact{Filename: name, S3Key: key, SHA256: sum, Size: st.Size()}, nil
}

// mergeArtifacts replaces the entries of old by their updated versions and
// appends updated entries that were not listed (bininfo.json).
func mergeArtifacts(old []indexer.Artifact, updated map[string]indexer.Artifact) []indexer.Artifact {
	out := make([]indexer.Artifact, 0, len(old)+1)
	seen := map[string]bool{}
	for _, a := range old {
		if u, ok := updated[a.Filename]; ok {
			a = u
		}
		seen[a.Filename] = true
		out = append(out, a)
	}
	for name, u := range updated {
		if !seen[name] {
			out = append(out, u)
		}
	}
	return out
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p) // #nosec G304 -- paths are inside the stage's private temp dir
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
