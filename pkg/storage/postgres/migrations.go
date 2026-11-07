package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var EmbedMigrations embed.FS

// Migrate runs all pending database migrations against the provided pgxpool.Pool.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	subFS, err := fs.Sub(EmbedMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("failed to create migration sub-filesystem: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, subFS)
	if err != nil {
		return fmt.Errorf("failed to create goose provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("failed to run goose migrations: %w", err)
	}

	return nil
}
