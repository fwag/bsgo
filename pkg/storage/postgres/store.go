package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
	"gopkg.in/yaml.v3"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, connStr string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}

	if _, err := pool.Exec(ctx, InitialSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to run initial schema: %w", err)
	}

	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) CreateProject(ctx context.Context, prj *manifest.ProjectManifest) error {
	data, err := yaml.Marshal(prj)
	if err != nil {
		return fmt.Errorf("failed to marshal project: %w", err)
	}

	query := `
		INSERT INTO projects (name, title, description, manifest_yaml)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (name) DO NOTHING
	`
	tag, err := s.pool.Exec(ctx, query, prj.Project.Name, prj.Project.Title, prj.Project.Description, string(data))
	if err != nil {
		return fmt.Errorf("failed to insert project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrConflict
	}
	return nil
}

func (s *PostgresStore) GetProject(ctx context.Context, name string) (*manifest.ProjectManifest, error) {
	var yamlStr string
	query := `SELECT manifest_yaml FROM projects WHERE name = $1`
	err := s.pool.QueryRow(ctx, query, name).Scan(&yamlStr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("failed to query project: %w", err)
	}

	return manifest.ParseProject([]byte(yamlStr))
}

func (s *PostgresStore) ListProjects(ctx context.Context) ([]*manifest.ProjectManifest, error) {
	query := `SELECT manifest_yaml FROM projects ORDER BY name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	defer rows.Close()

	var list []*manifest.ProjectManifest
	for rows.Next() {
		var yamlStr string
		if err := rows.Scan(&yamlStr); err != nil {
			return nil, err
		}
		prj, err := manifest.ParseProject([]byte(yamlStr))
		if err == nil {
			list = append(list, prj)
		}
	}
	return list, nil
}

func (s *PostgresStore) UpsertPackage(ctx context.Context, projectName string, pkg *manifest.PackageManifest) error {
	data, err := yaml.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("failed to marshal package: %w", err)
	}

	shard := manifest.ShardPath(pkg.Package.Name)
	query := `
		INSERT INTO project_packages (
			project_name, package_name, shard, git_url, branch, commit_sha, enabled,
			memory_limit, vcpus, disk_limit, timeout, manifest_yaml, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
		ON CONFLICT (project_name, package_name) DO UPDATE SET
			shard = EXCLUDED.shard,
			git_url = EXCLUDED.git_url,
			branch = EXCLUDED.branch,
			commit_sha = EXCLUDED.commit_sha,
			enabled = EXCLUDED.enabled,
			memory_limit = EXCLUDED.memory_limit,
			vcpus = EXCLUDED.vcpus,
			disk_limit = EXCLUDED.disk_limit,
			timeout = EXCLUDED.timeout,
			manifest_yaml = EXCLUDED.manifest_yaml,
			updated_at = NOW()
	`

	_, err = s.pool.Exec(ctx, query,
		projectName, pkg.Package.Name, shard, pkg.Source.Git, pkg.Source.Branch,
		pkg.Source.Commit, pkg.Build.Enabled, pkg.Resources.Memory, pkg.Resources.VCPUs,
		pkg.Resources.Disk, pkg.Resources.Timeout, string(data),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert package: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetPackage(ctx context.Context, projectName, packageName string) (*manifest.PackageManifest, error) {
	var yamlStr string
	query := `SELECT manifest_yaml FROM project_packages WHERE project_name = $1 AND package_name = $2`
	err := s.pool.QueryRow(ctx, query, projectName, packageName).Scan(&yamlStr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("failed to query package: %w", err)
	}

	return manifest.ParsePackage([]byte(yamlStr))
}

func (s *PostgresStore) ListPackages(ctx context.Context, projectName string) ([]*manifest.PackageManifest, error) {
	query := `SELECT manifest_yaml FROM project_packages WHERE project_name = $1 ORDER BY package_name`
	rows, err := s.pool.Query(ctx, query, projectName)
	if err != nil {
		return nil, fmt.Errorf("failed to list packages: %w", err)
	}
	defer rows.Close()

	var list []*manifest.PackageManifest
	for rows.Next() {
		var yamlStr string
		if err := rows.Scan(&yamlStr); err != nil {
			return nil, err
		}
		pkg, err := manifest.ParsePackage([]byte(yamlStr))
		if err == nil {
			list = append(list, pkg)
		}
	}
	return list, nil
}
