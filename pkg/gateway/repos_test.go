package gateway_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/openSUSE/bsgo/pkg/auth"
	"github.com/openSUSE/bsgo/pkg/gateway"
)

func setupTestStorage(t *testing.T) (string, *gateway.DiskRepoStorage) {
	t.Helper()
	tmpDir := t.TempDir()

	// Create test repo layout: repos/myproj/standard/x86_64/repodata/repomd.xml
	repoDir := filepath.Join(tmpDir, "repos", "myproj", "standard", "x86_64", "repodata")
	if err := os.MkdirAll(repoDir, 0750); err != nil {
		t.Fatalf("failed to create repoDir: %v", err)
	}

	xmlPath := filepath.Join(repoDir, "repomd.xml")
	if err := os.WriteFile(xmlPath, []byte("<repomd>test</repomd>"), 0600); err != nil {
		t.Fatalf("failed to write repomd.xml: %v", err)
	}

	rpmPath := filepath.Join(tmpDir, "repos", "myproj", "standard", "x86_64", "test-1.0.rpm")
	if err := os.WriteFile(rpmPath, []byte("rpmcontent"), 0600); err != nil {
		t.Fatalf("failed to write test rpm: %v", err)
	}

	return tmpDir, gateway.NewDiskRepoStorage(tmpDir)
}

func TestRepoServerPublicAccess(t *testing.T) {
	_, storage := setupTestStorage(t)
	authorizer := auth.NewAuthorizer()
	srv := gateway.NewRepoServer(storage, authorizer)

	// Public project should allow anonymous GET
	req := httptest.NewRequest(http.MethodGet, "/repos/myproj/standard/x86_64/repodata/repomd.xml", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/xml" {
		t.Errorf("expected Content-Type application/xml, got %s", w.Header().Get("Content-Type"))
	}
	if w.Body.String() != "<repomd>test</repomd>" {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestRepoServerPrivateAccessUnauthorized(t *testing.T) {
	_, storage := setupTestStorage(t)
	authorizer := auth.NewAuthorizer()
	authorizer.SetProjectPrivacy("myproj", true, []string{"alice"}, nil)

	srv := gateway.NewRepoServer(storage, authorizer)

	// Anonymous request should be rejected with 401
	req := httptest.NewRequest(http.MethodGet, "/repos/myproj/standard/x86_64/repodata/repomd.xml", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("expected WWW-Authenticate header")
	}
}

func TestRepoServerPrivateAccessAuthorizedWithToken(t *testing.T) {
	_, storage := setupTestStorage(t)
	authorizer := auth.NewAuthorizer()
	authorizer.SetProjectPrivacy("myproj", true, []string{"alice"}, nil)

	validator := gateway.NewSimpleTokenValidator()
	rawToken, pat, err := auth.GeneratePAT("alice", "test-token", []string{"project:read:myproj"})
	if err != nil {
		t.Fatalf("failed to generate PAT: %v", err)
	}
	validator.RegisterPAT(rawToken, pat)

	srv := gateway.NewRepoServer(storage, authorizer)
	srv.SetTokenValidator(validator)

	// Request with Bearer token
	req := httptest.NewRequest(http.MethodGet, "/repos/myproj/standard/x86_64/test-1.0.rpm", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/x-rpm" {
		t.Errorf("expected Content-Type application/x-rpm, got %s", w.Header().Get("Content-Type"))
	}

	// Request with Query Param token
	req2 := httptest.NewRequest(http.MethodGet, "/repos/myproj/standard/x86_64/test-1.0.rpm?token="+rawToken, nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected query param token status 200, got %d", w2.Code)
	}
}

func TestRepoServerPrivateAccessInsufficientScope(t *testing.T) {
	_, storage := setupTestStorage(t)
	authorizer := auth.NewAuthorizer()
	authorizer.SetProjectPrivacy("myproj", true, []string{"alice"}, nil)

	validator := gateway.NewSimpleTokenValidator()
	rawToken, pat, _ := auth.GeneratePAT("alice", "test-token", []string{"project:read:otherproj"})
	validator.RegisterPAT(rawToken, pat)

	srv := gateway.NewRepoServer(storage, authorizer)
	srv.SetTokenValidator(validator)

	req := httptest.NewRequest(http.MethodGet, "/repos/myproj/standard/x86_64/test-1.0.rpm", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 for insufficient scope, got %d", w.Code)
	}
}

func TestRepoServerPathTraversal(t *testing.T) {
	_, storage := setupTestStorage(t)
	authorizer := auth.NewAuthorizer()
	srv := gateway.NewRepoServer(storage, authorizer)

	req := httptest.NewRequest(http.MethodGet, "/repos/myproj/standard/x86_64/../../etc/passwd", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path traversal, got %d", w.Code)
	}
}

func TestRepoServerNotFound(t *testing.T) {
	_, storage := setupTestStorage(t)
	authorizer := auth.NewAuthorizer()
	srv := gateway.NewRepoServer(storage, authorizer)

	req := httptest.NewRequest(http.MethodGet, "/repos/myproj/standard/x86_64/nonexistent.rpm", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing file, got %d", w.Code)
	}
}
