package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// BuildMeta is the dependency meta of one build: the lines the build was
// scheduled with (see pkg/scheduler/checker), addressed by their hash, which
// PackageBuildState.Meta and BuiltMeta hold. A build that depends on the
// binaries of another build in the same repository includes these lines in
// its own meta, so they must outlive the build state row that produced them.
type BuildMeta struct {
	Hash  string
	Lines []string
}

// EncodeBuildMeta compresses the lines for storage.
func EncodeBuildMeta(lines []string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := io.WriteString(zw, strings.Join(lines, "\n")); err != nil {
		return nil, fmt.Errorf("compress build meta: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compress build meta: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeBuildMeta is the inverse of EncodeBuildMeta.
func DecodeBuildMeta(data []byte) ([]string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decompress build meta: %w", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("decompress build meta: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return strings.Split(string(raw), "\n"), nil
}

// SaveBuildMetas implements Store.
func (s *MemoryStore) SaveBuildMetas(_ context.Context, project, repo, arch string, metas []BuildMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := project + "/" + repo + "/" + arch
	if s.metas[key] == nil {
		s.metas[key] = map[string]storedMeta{}
	}
	for _, m := range metas {
		if _, ok := s.metas[key][m.Hash]; !ok {
			s.metas[key][m.Hash] = storedMeta{lines: append([]string(nil), m.Lines...), at: time.Now()}
		}
	}
	return nil
}

// GetBuildMetas implements Store. Unknown hashes are left out.
func (s *MemoryStore) GetBuildMetas(_ context.Context, project, repo, arch string, hashes []string) ([]BuildMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []BuildMeta
	for _, h := range hashes {
		if m, ok := s.metas[project+"/"+repo+"/"+arch][h]; ok {
			out = append(out, BuildMeta{Hash: h, Lines: append([]string(nil), m.lines...)})
		}
	}
	return out, nil
}

type storedMeta struct {
	lines []string
	at    time.Time
}

// PruneBuildMetas implements Store.
func (s *MemoryStore) PruneBuildMetas(_ context.Context, project, repo, arch string, keep []string, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := project + "/" + repo + "/" + arch
	keepSet := make(map[string]bool, len(keep))
	for _, h := range keep {
		keepSet[h] = true
	}
	for h, m := range s.metas[key] {
		if !keepSet[h] && m.at.Before(before) {
			delete(s.metas[key], h)
		}
	}
	return nil
}
