package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openSUSE/bsgo/pkg/auth"
)

var (
	ErrAccessDenied = errors.New("access denied")
	ErrInvalidPath  = errors.New("invalid repository path")
)

type RepoStorage interface {
	GetRepoFile(ctx context.Context, s3Key string) (io.ReadCloser, int64, string, error)
}

type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (*auth.User, []string, error)
}

type SimpleTokenValidator struct {
	tokens map[string]*auth.PersonalAccessToken
}

func NewSimpleTokenValidator() *SimpleTokenValidator {
	return &SimpleTokenValidator{tokens: make(map[string]*auth.PersonalAccessToken)}
}

func (v *SimpleTokenValidator) RegisterPAT(rawToken string, pat *auth.PersonalAccessToken) {
	v.tokens[rawToken] = pat
}

func (v *SimpleTokenValidator) ValidateToken(_ context.Context, rawToken string) (*auth.User, []string, error) {
	if pat, exists := v.tokens[rawToken]; exists {
		return &auth.User{ID: pat.UserID, Username: pat.UserID}, pat.Scopes, nil
	}
	return nil, nil, errors.New("invalid or expired token")
}

type DiskRepoStorage struct {
	rootDir string
}

func NewDiskRepoStorage(rootDir string) *DiskRepoStorage {
	return &DiskRepoStorage{rootDir: rootDir}
}

func (d *DiskRepoStorage) GetRepoFile(_ context.Context, s3Key string) (io.ReadCloser, int64, string, error) {
	cleanKey := filepath.Clean(s3Key)
	fullPath := filepath.Clean(filepath.Join(d.rootDir, cleanKey))
	rel, err := filepath.Rel(d.rootDir, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, 0, "", ErrInvalidPath
	}

	// #nosec G703 G304 -- validated safe path within rootDir
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, 0, "", err
	}
	if info.IsDir() {
		return nil, 0, "", errors.New("is a directory")
	}

	// #nosec G703 G304 -- validated safe path within rootDir
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, 0, "", err
	}

	return f, info.Size(), "", nil
}

type RepoServer struct {
	storage    RepoStorage
	authorizer *auth.Authorizer
	validator  TokenValidator
}

func NewRepoServer(storage RepoStorage, authorizer *auth.Authorizer) *RepoServer {
	return &RepoServer{
		storage:    storage,
		authorizer: authorizer,
	}
}

func (s *RepoServer) SetTokenValidator(v TokenValidator) {
	s.validator = v
}

func (s *RepoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	project, target, arch, subpath, err := parseRepoPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.authorize(r, project); err != nil {
		if errors.Is(err, ErrAccessDenied) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="bsgo"`)
			http.Error(w, "Unauthorized: private repository access denied", http.StatusUnauthorized)
			return
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	s3Key := fmt.Sprintf("repos/%s/%s/%s/%s", project, target, arch, subpath)
	rc, size, cType, err := s.storage.GetRepoFile(r.Context(), s3Key)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer rc.Close()

	contentType := detectContentType(subpath, cType)
	w.Header().Set("Content-Type", contentType)
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	_, _ = io.Copy(w, rc)
}

func parseRepoPath(path string) (project, target, arch, subpath string, err error) {
	trimmed := strings.TrimPrefix(path, "/repos/")
	trimmed = strings.TrimPrefix(trimmed, "repos/")
	trimmed = strings.Trim(trimmed, "/")

	parts := strings.SplitN(trimmed, "/", 4)
	if len(parts) < 4 {
		return "", "", "", "", fmt.Errorf("%w: expected /repos/{project}/{target}/{arch}/{filepath}", ErrInvalidPath)
	}

	project = parts[0]
	target = parts[1]
	arch = parts[2]
	subpath = filepath.Clean(parts[3])

	if strings.HasPrefix(subpath, "..") || strings.Contains(subpath, "..") {
		return "", "", "", "", fmt.Errorf("%w: path traversal detected", ErrInvalidPath)
	}

	return project, target, arch, subpath, nil
}

func (s *RepoServer) authorize(r *http.Request, project string) error {
	if s.authorizer == nil {
		return nil
	}

	// Anonymous check first
	if s.authorizer.CanRead(nil, project) {
		return nil
	}

	token := extractToken(r)
	if token == "" {
		return ErrAccessDenied
	}

	if s.validator != nil {
		user, scopes, err := s.validator.ValidateToken(r.Context(), token)
		if err != nil {
			return ErrAccessDenied
		}

		if len(scopes) > 0 && !auth.VerifyScope(scopes, "read", "project", project) {
			return errors.New("insufficient scope")
		}

		if user != nil && !s.authorizer.CanRead(user, project) {
			return errors.New("forbidden")
		}
		return nil
	}

	return ErrAccessDenied
}

func extractToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	}
	return r.URL.Query().Get("token")
}

func detectContentType(filename, currentType string) string {
	if currentType != "" && currentType != "application/octet-stream" {
		return currentType
	}

	switch {
	case strings.HasSuffix(filename, ".rpm"):
		return "application/x-rpm"
	case strings.HasSuffix(filename, ".xml"):
		return "application/xml"
	case strings.HasSuffix(filename, ".xml.gz"):
		return "application/x-gzip"
	case strings.HasSuffix(filename, ".asc"), strings.HasSuffix(filename, ".key"):
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}
