package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// historyChunk bounds the commits of one memo lookup.
const historyChunk = 1000

// NearestHistory implements storage.Store.
func (s *PostgresStore) NearestHistory(ctx context.Context, key storage.HistoryKey, commits []string) (*storage.SourceHistory, error) {
	for start := 0; start < len(commits); start += historyChunk {
		chunk := commits[start:min(start+historyChunk, len(commits))]
		found, err := s.historyRows(ctx, key, chunk)
		if err != nil {
			return nil, err
		}
		for _, c := range chunk {
			if h, ok := found[c]; ok {
				return h, nil
			}
		}
	}
	return nil, nil
}

func (s *PostgresStore) historyRows(ctx context.Context, key storage.HistoryKey, commits []string) (map[string]*storage.SourceHistory, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT commit_sha, main_recipe, version, release_start, vt, walked FROM source_history
		WHERE url = $1 AND lookup = $2 AND algo_rev = $3 AND config_hash = $4 AND commit_sha = ANY($5)`,
		key.URL, key.Lookup, key.AlgoRev, key.ConfigHash, commits)
	if err != nil {
		return nil, fmt.Errorf("failed to read source history: %w", err)
	}
	defer rows.Close()
	out := map[string]*storage.SourceHistory{}
	for rows.Next() {
		h := &storage.SourceHistory{URL: key.URL, Lookup: key.Lookup, ConfigHash: key.ConfigHash, AlgoRev: key.AlgoRev}
		var vt []byte
		if err := rows.Scan(&h.Commit, &h.MainRecipe, &h.Version, &h.ReleaseStart, &vt, &h.Walked); err != nil {
			return nil, fmt.Errorf("failed to scan source history: %w", err)
		}
		if err := json.Unmarshal(vt, &h.VT); err != nil {
			return nil, fmt.Errorf("failed to decode version table: %w", err)
		}
		out[h.Commit] = h
	}
	return out, rows.Err()
}

// PutHistory implements storage.Store. Two writers of the same commit compute
// the same row, so the first one wins and the other is a no-op.
func (s *PostgresStore) PutHistory(ctx context.Context, h storage.SourceHistory) error {
	if h.URL == "" || h.Commit == "" || h.Lookup == "" {
		return fmt.Errorf("source history requires url, commit and lookup")
	}
	vt, err := json.Marshal(h.VT)
	if err != nil {
		return fmt.Errorf("failed to encode version table: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO source_history (url, commit_sha, lookup, algo_rev, config_hash, main_recipe, version, release_start, vt, walked)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
		ON CONFLICT DO NOTHING`,
		h.URL, h.Commit, h.Lookup, h.AlgoRev, h.ConfigHash, h.MainRecipe, h.Version, h.ReleaseStart, string(vt), h.Walked)
	if err != nil {
		return fmt.Errorf("failed to store source history: %w", err)
	}
	return nil
}
