package main

import (
	"context"
	"testing"
)

func TestInitLockManagerFallback(t *testing.T) {
	ctx := context.Background()
	mgr := initLockManager(ctx, "")
	if mgr == nil {
		t.Fatalf("expected non-nil fallback lock manager")
	}

	unlock, err := mgr.AcquireLock(ctx, "proj:target:arch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	unlock()
}

func TestGetEnvFallback(t *testing.T) {
	val := getEnv("NON_EXISTENT_ENV_VAR_TEST", "fallback-val")
	if val != "fallback-val" {
		t.Errorf("expected fallback-val, got %s", val)
	}
}
