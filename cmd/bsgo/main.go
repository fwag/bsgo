package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
)

func main() {
	ctx := context.Background()
	store := initStore(ctx)

	rootCmd := cli.NewRootCommand(store)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func initStore(ctx context.Context) storage.Store {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = defaultDatabaseURL()
	}

	connectCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	pgStore, err := postgres.NewPostgresStore(connectCtx, dbURL)
	if err == nil {
		return pgStore
	}

	return storage.NewMemoryStore()
}

func defaultDatabaseURL() string {
	user := defaultEnv("PGUSER", "bsgo")
	pass := defaultEnv("PGPASSWORD", "password")
	host := defaultEnv("PGHOST", "localhost")
	port := defaultEnv("PGPORT", "5432")
	dbName := defaultEnv("PGDATABASE", "bsgodb")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, dbName)
}

func defaultEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
