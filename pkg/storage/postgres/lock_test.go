package postgres_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage/postgres"
)

func TestPostgresLockManagerNilPool(t *testing.T) {
	ctx := context.Background()
	mgr := postgres.NewPostgresLockManager(nil)

	unlock, err := mgr.AcquireLock(ctx, "project:target:arch")
	if err != nil {
		t.Fatalf("expected nil error on nil pool, got %v", err)
	}
	if unlock == nil {
		t.Fatalf("expected non-nil unlock function")
	}
	unlock() // verify no panic
}
