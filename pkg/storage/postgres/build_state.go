package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/openSUSE/bsgo/pkg/storage"
)

const upsertBuildStateSQL = `
	INSERT INTO package_build_state
		(project, repo, arch, package, flavor, state, details, source_commit, meta, bcnt, job_id, built_meta, built_commit, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6::varchar, $7, $8, $9::varchar, $10, $11,
		CASE WHEN $6::varchar = 'succeeded' THEN $9::varchar ELSE '' END,
		CASE WHEN $6::varchar = 'succeeded' THEN $8::varchar ELSE '' END, NOW())
	ON CONFLICT (project, repo, arch, package, flavor) DO UPDATE SET
		state = EXCLUDED.state,
		details = EXCLUDED.details,
		source_commit = COALESCE(NULLIF(EXCLUDED.source_commit, ''), package_build_state.source_commit),
		meta = COALESCE(NULLIF(EXCLUDED.meta, ''), package_build_state.meta),
		bcnt = GREATEST(EXCLUDED.bcnt, package_build_state.bcnt),
		job_id = EXCLUDED.job_id,
		built_meta = CASE WHEN EXCLUDED.state = 'succeeded'
			THEN COALESCE(NULLIF(EXCLUDED.meta, ''), package_build_state.meta)
			ELSE package_build_state.built_meta END,
		built_commit = CASE WHEN EXCLUDED.state = 'succeeded'
			THEN COALESCE(NULLIF(EXCLUDED.source_commit, ''), package_build_state.source_commit)
			ELSE package_build_state.built_commit END,
		updated_at = NOW()`

func upsertBuildStateArgs(st *storage.PackageBuildState) []any {
	return []any{st.Project, st.Repo, st.Arch, st.Package, st.Flavor,
		st.State, st.Details, st.SourceCommit, st.Meta, st.Bcnt, st.JobID}
}

// UpsertBuildState inserts or replaces the build state row for one package/flavor.
func (s *PostgresStore) UpsertBuildState(ctx context.Context, st *storage.PackageBuildState) error {
	if err := storage.ValidateBuildState(st); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, upsertBuildStateSQL, upsertBuildStateArgs(st)...); err != nil {
		return fmt.Errorf("failed to upsert build state: %w", err)
	}
	return nil
}

// GetBuildState returns one build state row or storage.ErrNotFound.
func (s *PostgresStore) GetBuildState(ctx context.Context, project, repo, arch, pkg, flavor string) (*storage.PackageBuildState, error) {
	var st storage.PackageBuildState
	err := s.pool.QueryRow(ctx, `
		SELECT project, repo, arch, package, flavor, state, details, source_commit, meta, built_meta, built_commit, bcnt, job_id, updated_at
		FROM package_build_state
		WHERE project = $1 AND repo = $2 AND arch = $3 AND package = $4 AND flavor = $5`,
		project, repo, arch, pkg, flavor).
		Scan(&st.Project, &st.Repo, &st.Arch, &st.Package, &st.Flavor, &st.State, &st.Details,
			&st.SourceCommit, &st.Meta, &st.BuiltMeta, &st.BuiltCommit, &st.Bcnt, &st.JobID, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read build state: %w", err)
	}
	return &st, nil
}

// UpsertBuildStates writes all rows in one transaction.
func (s *PostgresStore) UpsertBuildStates(ctx context.Context, states []*storage.PackageBuildState) error {
	for _, st := range states {
		if err := storage.ValidateBuildState(st); err != nil {
			return err
		}
	}
	if len(states) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin build state transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	batch := &pgx.Batch{}
	for _, st := range states {
		batch.Queue(upsertBuildStateSQL, upsertBuildStateArgs(st)...)
	}
	results := tx.SendBatch(ctx, batch)
	for range states {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("failed to upsert build states: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("failed to upsert build states: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit build states: %w", err)
	}
	return nil
}

const buildStateFilterClause = `
	WHERE project = $1
	  AND ($2::text = '' OR repo = $2::text)
	  AND ($3::text = '' OR arch = $3::text)
	  AND ($4::text = '' OR state = $4::text)`

// ListBuildStates returns the build states of a project ordered by repo, arch, package, flavor.
func (s *PostgresStore) ListBuildStates(ctx context.Context, project string, filter storage.BuildStateFilter) ([]*storage.PackageBuildState, error) {
	query := `
		SELECT project, repo, arch, package, flavor, state, details, source_commit, meta, built_meta, built_commit, bcnt, job_id, updated_at
		FROM package_build_state` + buildStateFilterClause + `
		ORDER BY repo, arch, package, flavor`
	rows, err := s.pool.Query(ctx, query, project, filter.Repo, filter.Arch, filter.State)
	if err != nil {
		return nil, fmt.Errorf("failed to list build states: %w", err)
	}
	defer rows.Close()

	list := []*storage.PackageBuildState{}
	for rows.Next() {
		var st storage.PackageBuildState
		if err := rows.Scan(&st.Project, &st.Repo, &st.Arch, &st.Package, &st.Flavor, &st.State,
			&st.Details, &st.SourceCommit, &st.Meta, &st.BuiltMeta, &st.BuiltCommit, &st.Bcnt, &st.JobID, &st.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan build state: %w", err)
		}
		list = append(list, &st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate build states: %w", err)
	}
	return list, nil
}

// CountBuildStates returns the number of rows per state for a project.
func (s *PostgresStore) CountBuildStates(ctx context.Context, project string, filter storage.BuildStateFilter) (map[string]int, error) {
	query := `SELECT state, count(*) FROM package_build_state` + buildStateFilterClause + ` GROUP BY state`
	rows, err := s.pool.Query(ctx, query, project, filter.Repo, filter.Arch, filter.State)
	if err != nil {
		return nil, fmt.Errorf("failed to count build states: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("failed to scan build state count: %w", err)
		}
		counts[state] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate build state counts: %w", err)
	}
	return counts, nil
}
