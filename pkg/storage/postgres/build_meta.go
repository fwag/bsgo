package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// SaveBuildMetas stores the metas in one batch. A hash that is already stored
// is left alone: metas are content-addressed.
func (s *PostgresStore) SaveBuildMetas(ctx context.Context, project, repo, arch string, metas []storage.BuildMeta) error {
	if len(metas) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, m := range metas {
		data, err := storage.EncodeBuildMeta(m.Lines)
		if err != nil {
			return err
		}
		batch.Queue(`INSERT INTO build_meta (project, repo, arch, hash, lines) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING`, project, repo, arch, m.Hash, data)
	}
	results := s.pool.SendBatch(ctx, batch)
	for range metas {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("failed to save build meta: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("failed to save build meta: %w", err)
	}
	return nil
}

// GetBuildMetas returns the stored metas among hashes.
func (s *PostgresStore) GetBuildMetas(ctx context.Context, project, repo, arch string, hashes []string) ([]storage.BuildMeta, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT hash, lines FROM build_meta
		WHERE project = $1 AND repo = $2 AND arch = $3 AND hash = ANY($4)`, project, repo, arch, hashes)
	if err != nil {
		return nil, fmt.Errorf("failed to read build metas: %w", err)
	}
	defer rows.Close()
	var out []storage.BuildMeta
	for rows.Next() {
		var hash string
		var data []byte
		if err := rows.Scan(&hash, &data); err != nil {
			return nil, fmt.Errorf("failed to scan build meta: %w", err)
		}
		lines, err := storage.DecodeBuildMeta(data)
		if err != nil {
			return nil, err
		}
		out = append(out, storage.BuildMeta{Hash: hash, Lines: lines})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate build metas: %w", err)
	}
	return out, nil
}

// PruneBuildMetas deletes the metas of one prpa that are not in keep.
func (s *PostgresStore) PruneBuildMetas(ctx context.Context, project, repo, arch string, keep []string, before time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM build_meta
		WHERE project = $1 AND repo = $2 AND arch = $3 AND NOT (hash = ANY($4)) AND created_at < $5`,
		project, repo, arch, keep, before); err != nil {
		return fmt.Errorf("failed to prune build metas: %w", err)
	}
	return nil
}
