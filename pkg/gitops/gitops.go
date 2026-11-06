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
	if err := os.MkdirAll(path, 0755); err != nil {
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
	cmd := exec.Command("git", "clone", bareRepoPath, workDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %s: %w", string(out), err)
	}

	return &Client{workDir: workDir}, nil
}

func (c *Client) CommitFile(ctx context.Context, relPath string, content []byte, msg, authorName, authorEmail string) (string, error) {
	fullPath := filepath.Join(c.workDir, relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create parent dir: %w", err)
	}

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	if err := c.runGit(ctx, "add", relPath); err != nil {
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
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s failed: %s: %w", strings.Join(args, " "), string(out), err)
	}
	return nil
}
