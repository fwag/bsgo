package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresLockManager struct {
	pool *pgxpool.Pool
}

func NewPostgresLockManager(pool *pgxpool.Pool) *PostgresLockManager {
	return &PostgresLockManager{pool: pool}
}

func (m *PostgresLockManager) AcquireLock(ctx context.Context, key string) (func(), error) {
	if m.pool == nil {
		return func() {}, nil
	}

	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire connection for advisory lock: %w", err)
	}

	// pg_advisory_lock acquires a session-level advisory lock using 64-bit hashtext
	_, err = conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", key)
	if err != nil {
		conn.Release()
		return nil, fmt.Errorf("failed to acquire pg_advisory_lock for %s: %w", key, err)
	}

	unlock := func() {
		defer conn.Release()
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", key)
	}

	return unlock, nil
}
