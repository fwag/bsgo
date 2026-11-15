package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// sandboxArgs are the obs-build options selecting and sizing the sandbox (KVM
// micro-VM, systemd-nspawn container or plain chroot). KVM runs get "-net none" unless the operator
// overrides the VM arguments.
func (s *ProcessSandbox) sandboxArgs(job *v1.BuildJob, buildRoot, vmDisk, vmSwap, cacheDir string) []string {
	switch s.SandboxMode {
	case "obs-build":
		return []string{"--clean", "--changelog", "--root", buildRoot, "--cachedir", cacheDir}
	case "nspawn":
		// build-vm-nspawn clears VM_ROOT and VM_SWAP and has no memory, disk
		// or user knobs: systemd-nspawn runs directly on the build root with
		// --private-network, so no --vm-disk/--vm-swap/--vm-memory/--vm-user.
		return []string{"--clean", "--changelog", "--vm-type", "nspawn", "--root", buildRoot, "--cachedir", cacheDir}
	}
	memMB := int64(job.Resources.MemoryMB)
	if memMB <= 0 {
		memMB = 4096
	}
	diskMB := int64(job.Resources.DiskGB) * 1024
	if diskMB <= 0 {
		diskMB = 10240
	}
	kvmUser := s.KVMUser
	if kvmUser == "" {
		kvmUser = "qemu"
	}
	vmArgs := s.KVMVMArgs
	if vmArgs == "" {
		vmArgs = "-sandbox on -net none"
	}
	return []string{
		"--clean",
		"--changelog",
		"--vm-type", "kvm",
		"--root", buildRoot,
		"--vm-disk", vmDisk,
		"--vm-swap", vmSwap,
		"--cachedir", cacheDir,
		"--vm-memory", strconv.FormatInt(memMB, 10),
		"--vm-disk-size", strconv.FormatInt(diskMB, 10),
		"--vm-user", kvmUser,
		"--vm-args", vmArgs,
	}
}

// buildKVMArgs builds the legacy (no BuildInfo) obs-build command line, which
// lets obs-build resolve dependencies from --repo.
func (s *ProcessSandbox) buildKVMArgs(ctx context.Context, job *v1.BuildJob, buildRoot, vmDisk, vmSwap, cacheDir, specFile, configPath string, w io.Writer) []string {
	vcpus := job.Resources.VCPUs
	if vcpus <= 0 {
		vcpus = 1
	}
	args := s.sandboxArgs(job, buildRoot, vmDisk, vmSwap, cacheDir)
	args = append(args, "--jobs", strconv.Itoa(int(vcpus)))

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

// writeLegacyConfig renders .build.config from the legacy job fields.
func writeLegacyConfig(job *v1.BuildJob, configPath string) {
	if checkFileExists(configPath) {
		return
	}
	if job.BuildConfig != "" {
		_ = os.WriteFile(configPath, []byte(job.BuildConfig), 0o600)
		return
	}
	hasLegacy := job.BuildEngine != "" || job.BuildType != "" || job.Macros != "" || len(job.BuildFlags) > 0 ||
		len(job.Prefer) > 0 || len(job.Preinstall) > 0 || len(job.Support) > 0 || len(job.Required) > 0
	if !hasLegacy {
		return
	}
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
		_ = os.WriteFile(configPath, []byte(rendered), 0o600)
	}
}

func (s *ProcessSandbox) obsBuildBin() string {
	if s.ObsBuildBin == "" {
		return "/usr/bin/build"
	}
	return s.ObsBuildBin
}

// jobBuildRoot returns the build root of a job. Its basename must be unique per
// job: obs-build derives the systemd-nspawn machine name from it
// ("obsbuild.<basename>", build-vm-nspawn), and two concurrent jobs with the
// same machine name make the second one fail ("Mount point ... exists
// already"). The job directory name is the job ID, a UUID, so "root-<id>" is
// unique and stays within the 64-character machine name limit.
func jobBuildRoot(jobDir string) string {
	return filepath.Join(jobDir, "root-"+filepath.Base(jobDir))
}

// buildArgsFor prepares the inputs and the obs-build command line of a job:
// the hermetic BuildInfo flow (fetch and verify bdeps, rpmlist, no --repo) or
// the legacy flow.
func (s *ProcessSandbox) buildArgsFor(ctx context.Context, job *v1.BuildJob, jobDir, specFile, vmDisk, vmSwap string, w io.Writer) ([]string, error) {
	buildRoot := jobBuildRoot(jobDir)
	if job.BuildInfo == nil {
		cacheDir := filepath.Join(s.WorkDir, "cache")
		_ = os.MkdirAll(cacheDir, 0o750)
		configPath := filepath.Join(jobDir, ".build.config")
		writeLegacyConfig(job, configPath)
		return s.buildKVMArgs(ctx, job, buildRoot, vmDisk, vmSwap, cacheDir, specFile, configPath, w), nil
	}
	configPath, rpmlistPath, err := s.prepareHermetic(ctx, job, jobDir, w)
	if err != nil {
		return nil, err
	}
	// A per-job obs-build cache: no state survives from one job to the next.
	cacheDir := filepath.Join(jobDir, "obs-cache")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, err
	}
	args := s.sandboxArgs(job, buildRoot, vmDisk, vmSwap, cacheDir)
	return append(args, hermeticArgs(job, configPath, rpmlistPath, specFile)...), nil
}

func (s *ProcessSandbox) buildWithKVM(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	obsBin := s.obsBuildBin()
	fmt.Fprintf(w, "[%s] bsgo-worker: preparing KVM micro-VM sandbox via %s\n", time.Now().Format("15:04:05"), obsBin)

	specFile, commit, err := s.setupKVMSources(ctx, job, jobDir, w)
	if err != nil {
		return nil, fmt.Errorf("failed setting up package sources: %w", err)
	}

	buildRoot := jobBuildRoot(jobDir)
	vmDisk, vmSwap, err := s.setupKVMDisks(job)
	if err != nil {
		return nil, fmt.Errorf("failed configuring VM disks: %w", err)
	}
	failed := true
	defer func() {
		if failed && s.KeepFailedJobs {
			fmt.Fprintf(w, "[%s] bsgo-worker: keeping failed job directory %s (keep_failed_jobs)\n", time.Now().Format("15:04:05"), jobDir)
			return
		}
		cleanupKVMRun(jobDir, buildRoot, vmDisk, vmSwap)
	}()

	args, err := s.buildArgsFor(ctx, job, jobDir, specFile, vmDisk, vmSwap, w)
	if err != nil {
		return nil, err
	}

	defer func() {
		if failed && s.KeepFailedJobs {
			return
		}
		wipeBuildRoot(context.Background(), obsBin, buildRoot, w)
		_ = os.RemoveAll(filepath.Join(jobDir, "repo_snapshot"))
		_ = os.RemoveAll(filepath.Join(jobDir, "packages"))
		_ = os.RemoveAll(filepath.Join(jobDir, "bdeps"))
		_ = os.RemoveAll(filepath.Join(jobDir, "obs-cache"))
	}()

	bin, runArgs := personalityCommand(PersonalityHelper(s.HostArch, job.Architecture), obsBin, args)
	fmt.Fprintf(w, "[%s] bsgo-worker: executing %s %s\n", time.Now().Format("15:04:05"), bin, strings.Join(runArgs, " "))
	run := runWatched(ctx, bin, runArgs, jobDir, limitsFor(job), w)
	if run.err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: KVM execution exited: %v\n", time.Now().Format("15:04:05"), run.err)
		res := &ExecutionResult{ExitCode: run.exitCode, Aborted: run.aborted, SourceCommit: commit}
		return res, run.err
	}

	res, err := s.collectKVMArtifacts(ctx, buildRoot, jobDir, job, commit, w, start)
	failed = err != nil
	return res, err
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

// buildLimits bound one obs-build run. Zero values disable a limit.
type buildLimits struct {
	timeout time.Duration
	idle    time.Duration
}

func limitsFor(job *v1.BuildJob) buildLimits {
	lim := buildLimits{timeout: time.Duration(job.Resources.TimeoutSeconds) * time.Second}
	if job.BuildInfo != nil {
		lim.idle = time.Duration(job.BuildInfo.LogIdleLimit) * time.Second
	}
	return lim
}

var (
	errIdleLimit    = errors.New("log idle limit exceeded")
	errBuildTimeout = errors.New("build timeout exceeded")
)

// runOutcome classifies one obs-build run. aborted distinguishes jobs stopped
// from the outside (timeout, idle limit, abort request, killed by a signal)
// from builds that genuinely failed.
type runOutcome struct {
	err      error
	exitCode int
	aborted  bool
}

// idleWriter forwards to w and remembers when output last arrived.
type idleWriter struct {
	w    io.Writer
	mu   sync.Mutex
	last time.Time
}

func (i *idleWriter) Write(p []byte) (int, error) {
	i.mu.Lock()
	i.last = time.Now()
	i.mu.Unlock()
	return i.w.Write(p)
}

func (i *idleWriter) idleFor() time.Duration {
	i.mu.Lock()
	defer i.mu.Unlock()
	return time.Since(i.last)
}

// watchIdle cancels the run when no output arrived for lim.idle.
func watchIdle(iw *idleWriter, idle time.Duration, cancel context.CancelCauseFunc, done <-chan struct{}) {
	tick := max(idle/10, 10*time.Millisecond)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if iw.idleFor() > idle {
				cancel(errIdleLimit)
				return
			}
		}
	}
}

// personalityCommand puts the personality helper in front of the obs-build
// command, like bs_worker does: obs-build itself never switches to the 32 bit
// personality, so without linux32 an i586 build on an x86_64 host would see
// uname -m = x86_64 and build 64 bit binaries. Under the helper obs-build
// reports the host as i686; the KVM VM type resets the personality for
// qemu itself (linux64 in build-vm-kvm).
func personalityCommand(helper, obsBin string, args []string) (string, []string) {
	if helper == "" {
		return obsBin, args
	}
	return helper, append([]string{obsBin}, args...)
}

// runWatched runs bin in its own process group, enforcing the timeout and the
// log idle limit, and classifies the outcome.
func runWatched(ctx context.Context, bin string, args []string, dir string, lim buildLimits, w io.Writer) runOutcome {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if lim.timeout > 0 {
		var cancelTimeout context.CancelFunc
		runCtx, cancelTimeout = context.WithTimeoutCause(runCtx, lim.timeout, errBuildTimeout)
		defer cancelTimeout()
	}

	iw := &idleWriter{w: w, last: time.Now()}
	// #nosec G204 -- obs-build runner executes parameterized sandbox binary
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dir
	cmd.Stdout = iw
	cmd.Stderr = iw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second

	done := make(chan struct{})
	defer close(done)
	if lim.idle > 0 {
		go watchIdle(iw, lim.idle, cancel, done)
	}

	err := cmd.Run()
	if err == nil {
		return runOutcome{}
	}
	return classifyRun(ctx, runCtx, err, lim)
}

func classifyRun(parent, runCtx context.Context, err error, lim buildLimits) runOutcome {
	cause := context.Cause(runCtx)
	switch {
	case errors.Is(cause, errIdleLimit):
		return runOutcome{aborted: true, exitCode: 137,
			err: fmt.Errorf("aborted: no build output for %s (logidlelimit)", lim.idle)}
	case errors.Is(cause, errBuildTimeout):
		return runOutcome{aborted: true, exitCode: 137,
			err: fmt.Errorf("aborted: build exceeded timeout of %s", lim.timeout)}
	case parent.Err() != nil:
		return runOutcome{aborted: true, exitCode: 137, err: errors.New("aborted: job cancelled")}
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return runOutcome{exitCode: 1, err: err}
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return runOutcome{aborted: true, exitCode: 128 + int(ws.Signal()),
			err: fmt.Errorf("aborted: build killed by signal %s", ws.Signal())}
	}
	return runOutcome{exitCode: max(ee.ExitCode(), 1), err: err}
}
