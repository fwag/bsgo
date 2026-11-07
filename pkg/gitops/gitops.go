package gitops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Client struct {
	workDir string
}

func InitBareRepo(path string) error {
	if err := os.MkdirAll(path, 0750); err != nil {
		return fmt.Errorf("failed to create directory for bare repo: %w", err)
	}

	cmd := exec.Command("git", "init", "--bare", "-b", "main")
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init --bare failed: %s: %w", string(out), err)
	}
	return nil
}

func CloneLocal(bareRepoPath, workDir string) (*Client, error) {
	// #nosec G204 -- git clone requires dynamic repository and working directory paths
	cmd := exec.Command("git", "clone", bareRepoPath, workDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %s: %w", string(out), err)
	}

	return &Client{workDir: workDir}, nil
}

func (c *Client) CommitFile(ctx context.Context, relPath string, content []byte, msg, authorName, authorEmail string) (string, error) {
	cleanPath := filepath.Clean(relPath)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(relPath) || strings.Contains(relPath, "\\") {
		return "", fmt.Errorf("invalid relative path (path traversal detected): %s", relPath)
	}

	fullPath := filepath.Join(c.workDir, cleanPath)
	relToWork, err := filepath.Rel(c.workDir, fullPath)
	if err != nil || strings.HasPrefix(relToWork, "..") {
		return "", fmt.Errorf("path escapes work directory: %s", relPath)
	}

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("failed to create parent dir: %w", err)
	}

	if err := os.WriteFile(fullPath, content, 0600); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	if err := c.runGit(ctx, "add", cleanPath); err != nil {
		return "", err
	}

	if err := validateGitIdentity(authorName, authorEmail); err != nil {
		return "", err
	}

	commitArgs := []string{
		"-c", "user.name=" + authorName,
		"-c", "user.email=" + authorEmail,
		"commit", "-m", msg,
	}
	if err := c.runGit(ctx, commitArgs...); err != nil {
		return "", err
	}

	if err := c.runGit(ctx, "push", "origin", "HEAD"); err != nil {
		return "", err
	}

	revCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	revCmd.Dir = c.workDir
	out, err := revCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get commit sha: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

func (c *Client) runGit(ctx context.Context, args ...string) error {
	// #nosec G204 -- git commands execute with repo-scoped arguments
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s failed: %s: %w", strings.Join(args, " "), string(out), err)
	}
	return nil
}

func validateGitIdentity(name, email string) error {
	if strings.ContainsAny(name, "\n\r\"<>=;[]\\") || len(name) > 128 || strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid author name: %q", name)
	}
	if strings.ContainsAny(email, "\n\r\"<>=;[]\\ \t") || len(email) > 128 || strings.TrimSpace(email) == "" {
		return fmt.Errorf("invalid author email: %q", email)
	}
	return nil
}
