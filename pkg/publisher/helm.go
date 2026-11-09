package publisher

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type HelmIndexOptions struct {
	Dir string
	URL string
}

type HelmIndexExecutor interface {
	Run(ctx context.Context, opts HelmIndexOptions) error
}

type RealHelmIndexExecutor struct {
	BinPath string
}

func NewRealHelmIndexExecutor() *RealHelmIndexExecutor {
	return &RealHelmIndexExecutor{
		BinPath: "helm",
	}
}

func (e *RealHelmIndexExecutor) Run(ctx context.Context, opts HelmIndexOptions) error {
	args := []string{"repo", "index"}
	if opts.URL != "" {
		args = append(args, "--url="+opts.URL)
	}
	args = append(args, opts.Dir)

	// #nosec G204 -- helm binary path and arguments are parameterized by design
	cmd := exec.CommandContext(ctx, e.BinPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm repo index failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
