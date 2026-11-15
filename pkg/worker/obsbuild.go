package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func (s *ProcessSandbox) buildKVMArgs(ctx context.Context, job *v1.BuildJob, buildRoot, vmDisk, vmSwap, cacheDir, specFile, configPath string, w io.Writer) []string {
	memMB := int64(job.Resources.MemoryMB)
	if memMB <= 0 {
		memMB = 4096
	}
	diskMB := int64(job.Resources.DiskGB) * 1024
	if diskMB <= 0 {
		diskMB = 10240
	}

	vcpus := job.Resources.VCPUs
	if vcpus <= 0 {
		vcpus = 1
	}

	var args []string
	if s.SandboxMode == "obs-build" {
		args = []string{
			"--clean",
			"--changelog",
			"--root", buildRoot,
			"--cachedir", cacheDir,
			"--jobs", strconv.Itoa(int(vcpus)),
		}
	} else {
		args = []string{
			"--clean",
			"--changelog",
			"--vm-type", "kvm",
			"--root", buildRoot,
			"--vm-disk", vmDisk,
			"--vm-swap", vmSwap,
			"--cachedir", cacheDir,
			"--vm-memory", strconv.FormatInt(memMB, 10),
			"--vm-disk-size", strconv.FormatInt(diskMB, 10),
			"--jobs", strconv.Itoa(int(vcpus)),
		}
		kvmUser := s.KVMUser
		if kvmUser == "" {
			kvmUser = "qemu"
		}
		args = append(args, "--vm-user", kvmUser)

		vmArgs := s.KVMVMArgs
		if vmArgs == "" {
			vmArgs = "-sandbox on -net none"
		}
		args = append(args, "--vm-args", vmArgs)
	}

	if checkFileExists(configPath) {
		args = append(args, "--dist", configPath)
	}

	repoArgs := s.ResolveJobRepositories(ctx, job, w)
	args = append(args, repoArgs...)
	if len(repoArgs) == 0 {
		args = append(args, "--repo", "zypp://")
	}
	args = append(args, specFile)
	return args
}

func (s *ProcessSandbox) buildWithKVM(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	obsBin := s.ObsBuildBin
	if obsBin == "" {
		obsBin = "/usr/bin/build"
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: preparing KVM micro-VM sandbox via %s\n", time.Now().Format("15:04:05"), obsBin)

	specFile, err := s.setupKVMSources(ctx, job, jobDir)
	if err != nil {
		return nil, fmt.Errorf("failed setting up package sources: %w", err)
	}

	cacheDir := filepath.Join(s.WorkDir, "cache")
	_ = os.MkdirAll(cacheDir, 0750)

	buildRoot := filepath.Join(jobDir, "build-root")
	vmDisk, vmSwap, err := s.setupKVMDisks(job)
	if err != nil {
		return nil, fmt.Errorf("failed configuring VM disks: %w", err)
	}

	defer cleanupKVMRun(jobDir, buildRoot, vmDisk, vmSwap)

	configPath := filepath.Join(jobDir, ".build.config")
	if !checkFileExists(configPath) {
		if job.BuildConfig != "" {
			_ = os.WriteFile(configPath, []byte(job.BuildConfig), 0600)
		} else if job.BuildEngine != "" || job.BuildType != "" || job.Macros != "" || len(job.BuildFlags) > 0 || len(job.Prefer) > 0 || len(job.Preinstall) > 0 || len(job.Support) > 0 || len(job.Required) > 0 {
			cfg := &manifest.ProjectConfig{
				BuildEngine: job.BuildEngine,
				BuildTypes:  []string{job.BuildType},
				Macros:      job.Macros,
				BuildFlags:  job.BuildFlags,
				Prefer:      job.Prefer,
				Preinstall:  job.Preinstall,
				VMinstall:   job.VMinstall,
				Support:     job.Support,
				Required:    job.Required,
				Order:       job.Order,
			}
			if rendered := RenderBuildConfig(cfg); rendered != "" {
				_ = os.WriteFile(configPath, []byte(rendered), 0600)
			}
		}
	}

	args := s.buildKVMArgs(ctx, job, buildRoot, vmDisk, vmSwap, cacheDir, specFile, configPath, w)

	defer func() {
		wipeBuildRoot(context.Background(), obsBin, buildRoot, w)
		_ = os.RemoveAll(filepath.Join(jobDir, "repo_snapshot"))
		_ = os.RemoveAll(filepath.Join(jobDir, "packages"))
	}()

	fmt.Fprintf(w, "[%s] bsgo-worker: executing %s %s\n", time.Now().Format("15:04:05"), obsBin, strings.Join(args, " "))
	// #nosec G204 -- obs-build runner executes parameterized sandbox binary
	cmd := exec.CommandContext(ctx, obsBin, args...)
	cmd.Dir = jobDir
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: KVM execution exited: %v\n", time.Now().Format("15:04:05"), err)
		return &ExecutionResult{ExitCode: 1}, err
	}

	return s.collectKVMArtifacts(ctx, buildRoot, jobDir, job, w, start)
}

func wipeBuildRoot(ctx context.Context, obsBin, buildRoot string, w io.Writer) {
	if buildRoot == "" || !checkFileExists(buildRoot) {
		return
	}
	// #nosec G204 -- obs-build runner executes parameterized sandbox binary
	cmd := exec.CommandContext(ctx, obsBin, "--root", buildRoot, "--wipe")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: failed to wipe build root: %v (%s)\n",
			time.Now().Format("15:04:05"), err, strings.TrimSpace(string(out)))
	}
}
