package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// NextBuildCounters allocates CI_CNT and B_CNT for one build. Allocation is
// serialized per (project, package, flavor) with a transaction-level advisory
// lock and idempotent per key, so a retried scheduling pass reuses the
// counters of the first attempt.
func (s *PostgresStore) NextBuildCounters(ctx context.Context, key storage.CounterKey) (storage.BuildCounters, error) {
	if err := storage.ValidateCounterKey(key); err != nil {
		return storage.BuildCounters{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return storage.BuildCounters{}, fmt.Errorf("failed to begin counter transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lock := "build_counters/" + key.Project + "/" + key.Package + "/" + key.Flavor
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", lock); err != nil {
		return storage.BuildCounters{}, fmt.Errorf("failed to lock build counters: %w", err)
	}
	var c storage.BuildCounters
	err = tx.QueryRow(ctx, `
		SELECT ci_cnt, bcnt FROM build_counters
		WHERE project = $1 AND repo = $2 AND arch = $3 AND package = $4 AND flavor = $5
		  AND source_commit = $6 AND meta = $7`,
		key.Project, key.Repo, key.Arch, key.Package, key.Flavor, key.SourceCommit, key.Meta).Scan(&c.CI, &c.B)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return storage.BuildCounters{}, fmt.Errorf("failed to read build counters: %w", err)
	}
	err = tx.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT max(ci_cnt) FROM build_counters
				WHERE project = $1 AND package = $4 AND flavor = $5 AND source_commit = $6),
				(SELECT COALESCE(max(ci_cnt), 0) + 1 FROM build_counters
				WHERE project = $1 AND package = $4 AND flavor = $5)),
			(SELECT COALESCE(max(bcnt), 0) + 1 FROM build_counters
				WHERE project = $1 AND repo = $2 AND arch = $3 AND package = $4 AND flavor = $5
				  AND source_commit = $6)`,
		key.Project, key.Repo, key.Arch, key.Package, key.Flavor, key.SourceCommit).Scan(&c.CI, &c.B)
	if err != nil {
		return storage.BuildCounters{}, fmt.Errorf("failed to compute build counters: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO build_counters (project, repo, arch, package, flavor, source_commit, meta, ci_cnt, bcnt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		key.Project, key.Repo, key.Arch, key.Package, key.Flavor, key.SourceCommit, key.Meta, c.CI, c.B)
	if err != nil {
		return storage.BuildCounters{}, fmt.Errorf("failed to store build counters: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storage.BuildCounters{}, fmt.Errorf("failed to commit build counters: %w", err)
	}
	return c, nil
}

// RequestForcedRebuild records a forced rebuild request; repeating it keeps the request and takes the latest priority.
func (s *PostgresStore) RequestForcedRebuild(ctx context.Context, req storage.ForcedRebuild) error {
	if req.Project == "" || req.Package == "" {
		return fmt.Errorf("forced rebuild requires project and package")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO build_force (project, repo, arch, package, flavor, priority)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project, repo, arch, package, flavor) DO UPDATE SET priority = EXCLUDED.priority`,
		req.Project, req.Repo, req.Arch, req.Package, req.Flavor, req.Priority)
	if err != nil {
		return fmt.Errorf("failed to record forced rebuild: %w", err)
	}
	return nil
}

// ListForcedRebuilds returns the pending forced rebuild requests of a project.
func (s *PostgresStore) ListForcedRebuilds(ctx context.Context, project string) ([]storage.ForcedRebuild, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT project, repo, arch, package, flavor, priority, requested_at
		FROM build_force WHERE project = $1 ORDER BY requested_at, package, flavor`, project)
	if err != nil {
		return nil, fmt.Errorf("failed to list forced rebuilds: %w", err)
	}
	defer rows.Close()
	var out []storage.ForcedRebuild
	for rows.Next() {
		var f storage.ForcedRebuild
		if err := rows.Scan(&f.Project, &f.Repo, &f.Arch, &f.Package, &f.Flavor, &f.Priority, &f.RequestedAt); err != nil {
			return nil, fmt.Errorf("failed to scan forced rebuild: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate forced rebuilds: %w", err)
	}
	return out, nil
}

// ClearForcedRebuilds removes the requests covering the given units.
func (s *PostgresStore) ClearForcedRebuilds(ctx context.Context, project, repo, arch string, units []storage.UnitRef) error {
	for _, u := range units {
		_, err := s.pool.Exec(ctx, `
			DELETE FROM build_force
			WHERE project = $1 AND package = $2 AND flavor = $3
			  AND (repo = '' OR repo = $4) AND (arch = '' OR arch = $5)`,
			project, u.Package, u.Flavor, repo, arch)
		if err != nil {
			return fmt.Errorf("failed to clear forced rebuild: %w", err)
		}
	}
	return nil
}

// SaveBuildRepoIndex inserts or replaces the index pointer of one prpa.
func (s *PostgresStore) SaveBuildRepoIndex(ctx context.Context, idx *storage.BuildRepoIndex) error {
	if err := storage.ValidateBuildRepoIndex(idx); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO build_repo_index (project, repo, arch, content_hash, solv_key, entries_key, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (project, repo, arch) DO UPDATE SET
			content_hash = EXCLUDED.content_hash,
			solv_key = EXCLUDED.solv_key,
			entries_key = EXCLUDED.entries_key,
			updated_at = NOW()`,
		idx.Project, idx.Repo, idx.Arch, idx.ContentHash, idx.SolvKey, idx.EntriesKey)
	if err != nil {
		return fmt.Errorf("failed to save build repo index: %w", err)
	}
	return nil
}

// GetBuildRepoIndex returns the index pointer of one prpa or storage.ErrNotFound.
func (s *PostgresStore) GetBuildRepoIndex(ctx context.Context, project, repo, arch string) (*storage.BuildRepoIndex, error) {
	var idx storage.BuildRepoIndex
	err := s.pool.QueryRow(ctx, `
		SELECT project, repo, arch, content_hash, solv_key, entries_key, updated_at
		FROM build_repo_index WHERE project = $1 AND repo = $2 AND arch = $3`, project, repo, arch).
		Scan(&idx.Project, &idx.Repo, &idx.Arch, &idx.ContentHash, &idx.SolvKey, &idx.EntriesKey, &idx.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read build repo index: %w", err)
	}
	return &idx, nil
}
