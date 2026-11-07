package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
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
		seedIfEmpty(ctx, pgStore)
		return pgStore
	}

	memStore := storage.NewMemoryStore()
	seedIfEmpty(ctx, memStore)
	return memStore
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

func seedIfEmpty(ctx context.Context, store storage.Store) {
	_, err := store.GetProject(ctx, "openSUSE:Factory")
	if err != nil {
		prj := &manifest.ProjectManifest{
			SchemaVersion: "v1",
			Project: manifest.ProjectInfo{
				Name:        "openSUSE:Factory",
				Title:       "openSUSE Factory Distribution",
				Description: "Rolling release base distribution",
			},
			Targets: map[string]manifest.BuildTarget{
				"standard": {
					Architectures: []string{"x86_64", "aarch64"},
					Flags: manifest.TargetFlags{
						Build:   true,
						Publish: true,
					},
				},
			},
		}
		_ = store.CreateProject(ctx, prj)
	}

	_, err = store.GetPackage(ctx, "openSUSE:Factory", "chromium")
	if err != nil {
		_ = store.UpsertPackage(ctx, "openSUSE:Factory", &manifest.PackageManifest{
			SchemaVersion: "v1",
			Package: manifest.PackageInfo{
				Name:  "chromium",
				Title: "Chromium Web Browser",
			},
			Source: manifest.PackageSource{
				Git:    "https://github.com/openSUSE/chromium.git",
				Branch: "factory",
				Commit: "8f7e2a9b4c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f",
			},
			Build: manifest.PackageBuild{
				Enabled: true,
			},
			Resources: manifest.ResourceLimits{
				Memory: "32Gi",
				VCPUs:  16,
				Disk:   "100Gi",
			},
		})

		_ = store.UpsertPackage(ctx, "openSUSE:Factory", &manifest.PackageManifest{
			SchemaVersion: "v1",
			Package: manifest.PackageInfo{
				Name:  "glibc",
				Title: "GNU C Library",
			},
			Source: manifest.PackageSource{
				Git:    "https://github.com/openSUSE/glibc.git",
				Branch: "factory",
			},
			Build: manifest.PackageBuild{
				Enabled: true,
			},
			Resources: manifest.ResourceLimits{
				Memory: "8Gi",
				VCPUs:  8,
			},
		})
	}
}
