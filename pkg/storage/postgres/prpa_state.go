package postgres

import (
	"context"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// SetPrpaState inserts or replaces the repository state of one prpa.
func (s *PostgresStore) SetPrpaState(ctx context.Context, st *storage.PrpaState) error {
	if err := storage.ValidatePrpaState(st); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO prpa_state (project, repo, arch, state, details, index_hash, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (project, repo, arch) DO UPDATE SET
			state = EXCLUDED.state,
			details = EXCLUDED.details,
			index_hash = EXCLUDED.index_hash,
			updated_at = NOW()`,
		st.Project, st.Repo, st.Arch, st.State, st.Details, st.IndexHash)
	if err != nil {
		return fmt.Errorf("failed to save prpa state: %w", err)
	}
	return nil
}

// GetPrpaState returns the repository state of one prpa or storage.ErrNotFound.
func (s *PostgresStore) GetPrpaState(ctx context.Context, project, repo, arch string) (*storage.PrpaState, error) {
	rows, err := s.ListPrpaStates(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, st := range rows {
		if st.Repo == repo && st.Arch == arch {
			return st, nil
		}
	}
	return nil, storage.ErrNotFound
}

// ListPrpaStates returns the repository states of a project ordered by repo and arch.
func (s *PostgresStore) ListPrpaStates(ctx context.Context, project string) ([]*storage.PrpaState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT project, repo, arch, state, details, index_hash, updated_at
		FROM prpa_state WHERE project = $1 ORDER BY repo, arch`, project)
	if err != nil {
		return nil, fmt.Errorf("failed to list prpa states: %w", err)
	}
	defer rows.Close()
	var out []*storage.PrpaState
	for rows.Next() {
		var st storage.PrpaState
		if err := rows.Scan(&st.Project, &st.Repo, &st.Arch, &st.State, &st.Details, &st.IndexHash, &st.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan prpa state: %w", err)
		}
		out = append(out, &st)
	}
	return out, rows.Err()
}
