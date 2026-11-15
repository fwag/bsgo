package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// SaveDoDRepo inserts or replaces the pointer row of one remote repository.
func (s *PostgresStore) SaveDoDRepo(ctx context.Context, repo *storage.DoDRepo) error {
	if repo.URL == "" || repo.RepomdChecksum == "" || repo.SolvKey == "" {
		return fmt.Errorf("dod repo requires url, repomd checksum and solv key")
	}
	query := `
		INSERT INTO dod_repo (url, name, repomd_checksum, solv_key, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (url) DO UPDATE SET
			name = EXCLUDED.name,
			repomd_checksum = EXCLUDED.repomd_checksum,
			solv_key = EXCLUDED.solv_key,
			updated_at = NOW()`
	if _, err := s.pool.Exec(ctx, query, repo.URL, repo.Name, repo.RepomdChecksum, repo.SolvKey); err != nil {
		return fmt.Errorf("failed to save dod repo: %w", err)
	}
	return nil
}

// GetDoDRepo returns the pointer row of url or storage.ErrNotFound.
func (s *PostgresStore) GetDoDRepo(ctx context.Context, url string) (*storage.DoDRepo, error) {
	query := `
		SELECT url, name, repomd_checksum, solv_key, updated_at
		FROM dod_repo WHERE url = $1`
	var r storage.DoDRepo
	err := s.pool.QueryRow(ctx, query, url).Scan(&r.URL, &r.Name, &r.RepomdChecksum, &r.SolvKey, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("failed to query dod repo: %w", err)
	}
	return &r, nil
}
