package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// revisionColumns lists the columns scanRevision reads.
const revisionColumns = `project, package, seq, git_url, commit_sha, state,
	COALESCE(version, ''), COALESCE(git_vrev, 0), COALESCE(vrev, 0), COALESCE(min_vrev, 0),
	COALESCE(source, ''), attempts, COALESCE(last_error, ''), next_try_at,
	COALESCE(claimed_by, ''), claimed_until, wanted_at, created_at, resolved_at`

func scanRevision(row pgx.Row) (*storage.PackageRevision, error) {
	var r storage.PackageRevision
	var next, claimed, wanted, resolved *time.Time
	err := row.Scan(&r.Project, &r.Package, &r.Seq, &r.GitURL, &r.Commit, &r.State,
		&r.Version, &r.GitVrev, &r.Vrev, &r.MinVrev, &r.Source, &r.Attempts, &r.LastError,
		&next, &r.ClaimedBy, &claimed, &wanted, &r.CreatedAt, &resolved)
	if err != nil {
		return nil, err
	}
	for dst, src := range map[*time.Time]*time.Time{&r.NextTryAt: next, &r.ClaimedUntil: claimed, &r.WantedAt: wanted, &r.ResolvedAt: resolved} {
		if src != nil {
			*dst = *src
		}
	}
	return &r, nil
}

func revisionLock(ctx context.Context, tx pgx.Tx, project, pkg string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "pkgrev/"+project+"/"+pkg)
	if err != nil {
		return fmt.Errorf("failed to lock package revisions: %w", err)
	}
	return nil
}

// appendRevision appends a pending revision when the pin differs from the
// latest one. It runs in the transaction of UpsertPackage.
func appendRevision(ctx context.Context, tx pgx.Tx, project, pkg, url, commit string) error {
	if url == "" || commit == "" {
		return nil
	}
	if err := revisionLock(ctx, tx, project, pkg); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO package_revisions (project, package, seq, git_url, commit_sha)
		SELECT $1::varchar, $2::varchar, COALESCE(max(seq), 0) + 1, $3::text, $4::varchar FROM package_revisions
		WHERE project = $1 AND package = $2
		HAVING NOT EXISTS (
			SELECT 1 FROM package_revisions l WHERE l.project = $1 AND l.package = $2
			  AND l.git_url = $3 AND l.commit_sha = $4
			  AND l.seq = (SELECT max(seq) FROM package_revisions WHERE project = $1 AND package = $2))`,
		project, pkg, url, commit)
	if err != nil {
		return fmt.Errorf("failed to append package revision: %w", err)
	}
	return nil
}

// RevisionFor implements storage.Store.
func (s *PostgresStore) RevisionFor(ctx context.Context, project, pkg, commit string) (*storage.PackageRevision, error) {
	r, err := scanRevision(s.pool.QueryRow(ctx, `
		SELECT `+revisionColumns+` FROM package_revisions
		WHERE project = $1 AND package = $2 AND commit_sha = $3 ORDER BY seq DESC LIMIT 1`,
		project, pkg, commit))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read package revision: %w", err)
	}
	return r, nil
}

// ListRevisions implements storage.Store.
func (s *PostgresStore) ListRevisions(ctx context.Context, project string, packages []string) ([]storage.PackageRevision, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+revisionColumns+` FROM package_revisions
		WHERE project = $1 AND (COALESCE(cardinality($2::text[]), 0) = 0 OR package = ANY($2))
		ORDER BY package, seq`, project, packages)
	if err != nil {
		return nil, fmt.Errorf("failed to list package revisions: %w", err)
	}
	defer rows.Close()
	var out []storage.PackageRevision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan package revision: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// WantRevisions implements storage.Store.
func (s *PostgresStore) WantRevisions(ctx context.Context, refs []storage.RevisionRef) error {
	if len(refs) == 0 {
		return nil
	}
	projects, pkgs, commits := make([]string, len(refs)), make([]string, len(refs)), make([]string, len(refs))
	for i, r := range refs {
		projects[i], pkgs[i], commits[i] = r.Project, r.Package, r.Commit
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE package_revisions r SET wanted_at = now()
		FROM unnest($1::text[], $2::text[], $3::text[]) AS w(project, package, commit_sha)
		WHERE r.project = w.project AND r.package = w.package AND r.commit_sha = w.commit_sha
		  AND r.state = 'pending'
		  AND (r.wanted_at IS NULL OR r.wanted_at < now() - interval '1 minute')`,
		projects, pkgs, commits)
	if err != nil {
		return fmt.Errorf("failed to record wanted revisions: %w", err)
	}
	return nil
}

// claimSQL leases the lowest pending revision of up to $2 packages, the ones
// units wait for first. SKIP LOCKED keeps concurrent replicas from blocking
// each other.
const claimSQL = `
	WITH cand AS (
		SELECT r.project, r.package, r.seq,
			EXISTS (SELECT 1 FROM package_revisions w
				WHERE w.project = r.project AND w.package = r.package AND w.state = 'pending'
				  AND w.wanted_at > now() - make_interval(secs => $4)) AS wanted
		FROM package_revisions r
		WHERE r.state = 'pending'
		  AND (r.next_try_at IS NULL OR r.next_try_at <= now())
		  AND (r.claimed_until IS NULL OR r.claimed_until < now())
		  AND r.seq = (SELECT min(m.seq) FROM package_revisions m
			WHERE m.project = r.project AND m.package = r.package AND m.state = 'pending')
		ORDER BY wanted DESC, r.created_at, r.project, r.package
		LIMIT $2
		FOR UPDATE OF r SKIP LOCKED)
	UPDATE package_revisions p
	SET claimed_by = $1, claimed_until = now() + make_interval(secs => $3)
	FROM cand
	WHERE p.project = cand.project AND p.package = cand.package AND p.seq = cand.seq
	RETURNING p.project, p.package, p.seq, p.git_url, p.commit_sha, p.state,
		COALESCE(p.version, ''), COALESCE(p.git_vrev, 0), COALESCE(p.vrev, 0), COALESCE(p.min_vrev, 0),
		COALESCE(p.source, ''), p.attempts, COALESCE(p.last_error, ''), p.next_try_at,
		COALESCE(p.claimed_by, ''), p.claimed_until, p.wanted_at, p.created_at, p.resolved_at`

// ClaimRevisions implements storage.Store.
func (s *PostgresStore) ClaimRevisions(ctx context.Context, owner string, limit int, lease time.Duration) ([]storage.PackageRevision, error) {
	rows, err := s.pool.Query(ctx, claimSQL, owner, limit, lease.Seconds(), storage.WantedWindow.Seconds())
	if err != nil {
		return nil, fmt.Errorf("failed to claim package revisions: %w", err)
	}
	defer rows.Close()
	var out []storage.PackageRevision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan claimed revision: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to claim package revisions: %w", err)
	}
	return out, nil
}

// lockPending locks the package and returns the stored row of rev when it is
// still pending and the lowest pending revision of its package.
func lockPending(ctx context.Context, tx pgx.Tx, rev storage.PackageRevision) (*storage.PackageRevision, error) {
	if err := revisionLock(ctx, tx, rev.Project, rev.Package); err != nil {
		return nil, err
	}
	row, err := scanRevision(tx.QueryRow(ctx, `
		SELECT `+revisionColumns+` FROM package_revisions
		WHERE project = $1 AND package = $2 AND state = 'pending' ORDER BY seq LIMIT 1`,
		rev.Project, rev.Package))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storage.ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read pending revision: %w", err)
	}
	if row.Seq != rev.Seq || row.Commit != rev.Commit {
		return nil, storage.ErrConflict
	}
	return row, nil
}

// FinishRevision implements storage.Store.
func (s *PostgresStore) FinishRevision(ctx context.Context, rev storage.PackageRevision, res storage.RevisionResult) (*storage.PackageRevision, error) {
	if res.Version == "" || res.GitVrev < 1 {
		return nil, fmt.Errorf("revision result requires a version and a git vrev")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin revision transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := lockPending(ctx, tx, rev)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT `+revisionColumns+` FROM package_revisions
		WHERE project = $1 AND package = $2 AND seq < $3 AND state = 'resolved'`,
		rev.Project, rev.Package, rev.Seq)
	if err != nil {
		return nil, fmt.Errorf("failed to read earlier revisions: %w", err)
	}
	var earlier []storage.PackageRevision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan earlier revision: %w", err)
		}
		earlier = append(earlier, *r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read earlier revisions: %w", err)
	}
	vrev := storage.ResolveVrev(earlier, res.Version, res.GitVrev, row.MinVrev)
	out, err := scanRevision(tx.QueryRow(ctx, `
		UPDATE package_revisions SET state = 'resolved', version = $4, git_vrev = $5, vrev = $6,
			source = 'git', last_error = NULL, claimed_by = NULL, claimed_until = NULL, resolved_at = now()
		WHERE project = $1 AND package = $2 AND seq = $3
		RETURNING `+revisionColumns, rev.Project, rev.Package, rev.Seq, res.Version, res.GitVrev, vrev))
	if err != nil {
		return nil, fmt.Errorf("failed to finish revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit revision: %w", err)
	}
	return out, nil
}

// FailRevision implements storage.Store.
func (s *PostgresStore) FailRevision(ctx context.Context, rev storage.PackageRevision, cause string, minRetry time.Duration) (*storage.PackageRevision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin revision transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := lockPending(ctx, tx, rev)
	if err != nil {
		return nil, err
	}
	var latest int
	if err := tx.QueryRow(ctx, `SELECT max(seq) FROM package_revisions WHERE project = $1 AND package = $2`,
		rev.Project, rev.Package).Scan(&latest); err != nil {
		return nil, fmt.Errorf("failed to read latest revision: %w", err)
	}
	attempts := row.Attempts + 1
	state := storage.RevisionPending
	if row.Seq != latest && attempts >= storage.MaxRevisionAttempts {
		state = storage.RevisionSkipped
	}
	wait := max(storage.RevisionBackoff(attempts), minRetry)
	out, err := scanRevision(tx.QueryRow(ctx, `
		UPDATE package_revisions SET attempts = $4, last_error = $5, state = $6,
			next_try_at = now() + make_interval(secs => $7), claimed_by = NULL, claimed_until = NULL
		WHERE project = $1 AND package = $2 AND seq = $3
		RETURNING `+revisionColumns, rev.Project, rev.Package, rev.Seq, attempts, cause, state, wait.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("failed to fail revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit revision: %w", err)
	}
	return out, nil
}

// RevisionCounts implements storage.Store.
func (s *PostgresStore) RevisionCounts(ctx context.Context) (storage.RevisionCounts, error) {
	var c storage.RevisionCounts
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state = 'pending'), count(*) FILTER (WHERE state = 'resolved'),
			count(*) FILTER (WHERE state = 'skipped') FROM package_revisions`).Scan(&c.Pending, &c.Resolved, &c.Skipped)
	if err != nil {
		return c, fmt.Errorf("failed to count package revisions: %w", err)
	}
	return c, nil
}

// ResetRevisions implements storage.Store.
func (s *PostgresStore) ResetRevisions(ctx context.Context, project string, packages []string, allowDecrease bool) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin reset transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		DELETE FROM source_history WHERE url IN (
			SELECT git_url FROM package_revisions WHERE project = $1
			  AND (COALESCE(cardinality($2::text[]), 0) = 0 OR package = ANY($2)))`, project, packages); err != nil {
		return nil, fmt.Errorf("failed to drop source history: %w", err)
	}
	rows, err := tx.Query(ctx, `
		UPDATE package_revisions r SET state = 'pending',
			min_vrev = CASE WHEN $3 THEN NULL ELSE r.vrev END,
			vrev = NULL, source = NULL, attempts = 0, last_error = NULL, next_try_at = NULL,
			resolved_at = NULL, claimed_by = NULL, claimed_until = NULL
		WHERE r.project = $1 AND r.state <> 'pending'
		  AND (COALESCE(cardinality($2::text[]), 0) = 0 OR r.package = ANY($2))
		  AND r.seq = (SELECT max(l.seq) FROM package_revisions l WHERE l.project = r.project AND l.package = r.package)
		RETURNING r.package`, project, packages, allowDecrease)
	if err != nil {
		return nil, fmt.Errorf("failed to reset package revisions: %w", err)
	}
	var done []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan reset package: %w", err)
		}
		done = append(done, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to reset package revisions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit reset: %w", err)
	}
	sort.Strings(done)
	return done, nil
}

// SetRevisionVrev implements storage.Store.
func (s *PostgresStore) SetRevisionVrev(ctx context.Context, project, pkg, commit, version string, vrev int) (*storage.PackageRevision, error) {
	if vrev < 1 {
		return nil, fmt.Errorf("vrev must be at least 1")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin revision transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := revisionLock(ctx, tx, project, pkg); err != nil {
		return nil, err
	}
	cur, err := scanRevision(tx.QueryRow(ctx, `
		SELECT `+revisionColumns+` FROM package_revisions
		WHERE project = $1 AND package = $2 ORDER BY seq DESC LIMIT 1`, project, pkg))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read latest revision: %w", err)
	}
	if commit != "" && cur.Commit != commit {
		return nil, storage.ErrConflict
	}
	if version == "" {
		version = cur.Version
	}
	if version == "" {
		return nil, fmt.Errorf("the revision has no version yet: pass one")
	}
	out, err := scanRevision(tx.QueryRow(ctx, `
		UPDATE package_revisions SET state = 'resolved', version = $4, vrev = $5, source = 'admin',
			claimed_by = NULL, claimed_until = NULL, resolved_at = now()
		WHERE project = $1 AND package = $2 AND seq = $3
		RETURNING `+revisionColumns, project, pkg, cur.Seq, version, vrev))
	if err != nil {
		return nil, fmt.Errorf("failed to set revision vrev: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit revision: %w", err)
	}
	return out, nil
}

// RenewRevisionClaim implements storage.Store.
func (s *PostgresStore) RenewRevisionClaim(ctx context.Context, rev storage.PackageRevision, owner string, lease time.Duration) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE package_revisions SET claimed_until = now() + make_interval(secs => $5)
		WHERE project = $1 AND package = $2 AND seq = $3 AND state = 'pending' AND claimed_by = $4`,
		rev.Project, rev.Package, rev.Seq, owner, lease.Seconds())
	if err != nil {
		return fmt.Errorf("failed to renew revision claim: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrConflict
	}
	return nil
}

// ReleaseRevisionClaims implements storage.Store.
func (s *PostgresStore) ReleaseRevisionClaims(ctx context.Context, owner string) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE package_revisions SET claimed_by = NULL, claimed_until = NULL
		WHERE claimed_by = $1 AND state = 'pending'`, owner)
	if err != nil {
		return 0, fmt.Errorf("failed to release revision claims: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
