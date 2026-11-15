package postgres

import (
	"context"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// UpsertBuildState inserts or replaces the build state row for one package/flavor.
func (s *PostgresStore) UpsertBuildState(ctx context.Context, st *storage.PackageBuildState) error {
	if st.Project == "" || st.Repo == "" || st.Arch == "" || st.Package == "" {
		return fmt.Errorf("build state requires project, repo, arch and package")
	}
	if !storage.IsValidBuildState(st.State) {
		return fmt.Errorf("invalid build state %q", st.State)
	}
	query := `
		INSERT INTO package_build_state
			(project, repo, arch, package, flavor, state, details, source_commit, meta, bcnt, job_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
		ON CONFLICT (project, repo, arch, package, flavor) DO UPDATE SET
			state = EXCLUDED.state,
			details = EXCLUDED.details,
			source_commit = COALESCE(NULLIF(EXCLUDED.source_commit, ''), package_build_state.source_commit),
			meta = COALESCE(NULLIF(EXCLUDED.meta, ''), package_build_state.meta),
			bcnt = GREATEST(EXCLUDED.bcnt, package_build_state.bcnt),
			job_id = EXCLUDED.job_id,
			updated_at = NOW()`
	_, err := s.pool.Exec(ctx, query, st.Project, st.Repo, st.Arch, st.Package, st.Flavor,
		st.State, st.Details, st.SourceCommit, st.Meta, st.Bcnt, st.JobID)
	if err != nil {
		return fmt.Errorf("failed to upsert build state: %w", err)
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
		SELECT project, repo, arch, package, flavor, state, details, source_commit, meta, bcnt, job_id, updated_at
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
			&st.Details, &st.SourceCommit, &st.Meta, &st.Bcnt, &st.JobID, &st.UpdatedAt); err != nil {
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
