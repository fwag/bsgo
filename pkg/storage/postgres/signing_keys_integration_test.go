package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
)

func TestProjectSigningKeysOpenPGPColumns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, getTestDatabaseURL())
	if err != nil {
		t.Skipf("skipping postgres integration test: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skipping postgres integration test (ping failed): %v", err)
	}
	store, err := postgres.NewPostgresStore(ctx, getTestDatabaseURL())
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	defer store.Close()

	name := fmt.Sprintf("home:keytest-%d", time.Now().UnixNano())
	if err := store.CreateProject(ctx, &manifest.ProjectManifest{
		SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: name, Title: "t"},
	}); err != nil {
		t.Fatalf("creating project: %v", err)
	}

	// A row as written before the OpenPGP migration: no algo, no expiry.
	_, err = pool.Exec(ctx, `INSERT INTO project_signing_keys
		(project_name, key_flavor, key_type, public_key_armored, encrypted_private_key, fingerprint)
		VALUES ($1, 'legacy', 'gpg', 'pem', 'enc', 'fp')`, name)
	if err != nil {
		t.Fatalf("inserting legacy row: %v", err)
	}
	legacy, err := store.GetProjectKey(ctx, name, "legacy", "gpg")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Algo != "" || !legacy.ExpiresAt.IsZero() {
		t.Errorf("legacy row should have empty algo and no expiry: %+v", legacy)
	}

	expires := time.Now().Add(800 * 24 * time.Hour).UTC().Truncate(time.Second)
	want := &storage.ProjectSigningKey{
		ProjectName: name, KeyFlavor: "default", KeyType: "gpg",
		PublicKeyArmored: "armored", EncryptedPrivateKey: "enc2",
		Fingerprint: "ABCDEF", Algo: "rsa4096", ExpiresAt: expires,
	}
	if err := store.SaveProjectKey(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProjectKey(ctx, name, "default", "gpg")
	if err != nil {
		t.Fatal(err)
	}
	if got.Algo != "rsa4096" || !got.ExpiresAt.Equal(expires) || got.Fingerprint != "ABCDEF" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	list, err := store.ListProjectKeys(ctx, name)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d keys, err %v", len(list), err)
	}
	if _, err := store.GetProjectKey(ctx, name, "missing", "gpg"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
