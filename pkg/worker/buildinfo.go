package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

var commitIDPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// checkHermeticAllowed enforces the isolation policy for BuildInfo jobs.
//
// nspawn mode runs systemd-nspawn with --private-network and is isolated too.
// KVM mode runs the build in a micro-VM started with "-net none". obs-build
// chroot mode ("obs-build") has no network isolation at all: the build runs
// with the worker's own network namespace. A BuildInfo job promises a
// hermetic build, so it is refused in chroot mode unless the operator
// explicitly accepts the risk (AllowUnisolatedChroot, default deny). Any
// other mode (the host rpmbuild development path) is always refused.
func (s *ProcessSandbox) checkHermeticAllowed(job *v1.BuildJob) error {
	switch strings.ToLower(strings.TrimSpace(job.BuildEngine)) {
	case "podman", "helm":
		return nil
	}
	switch s.SandboxMode {
	case "kvm", "nspawn":
		return nil
	case "obs-build":
		if s.AllowUnisolatedChroot {
			return nil
		}
		return errors.New("buildinfo jobs require network isolation: sandbox mode obs-build (plain chroot) has none; " +
			"use sandbox mode kvm or set sandbox.allow_unisolated_chroot")
	default:
		return fmt.Errorf("buildinfo jobs require sandbox mode kvm, worker runs %q", s.SandboxMode)
	}
}

// validateBuildInfo checks the scheduler-provided fields the worker must not
// invent. Bdeps are validated individually while they are fetched.
func validateBuildInfo(job *v1.BuildJob) error {
	bi := job.BuildInfo
	switch {
	case bi.Config == "":
		return errors.New("buildinfo has no config")
	case bi.ArchPath == "":
		return errors.New("buildinfo has no archpath")
	case !commitIDPattern.MatchString(bi.SourceCommit):
		return fmt.Errorf("buildinfo source_commit %q is not a full git commit id", bi.SourceCommit)
	case job.Source.GitURL == "":
		return errors.New("buildinfo job has no source git_url")
	}
	return nil
}

// renderRPMList produces the build script rpmlist exactly like bs_worker
// (src/backend/bs_worker, "build rpmlist for build script"):
//
//	<name> <path>            one line per bdep, in bdep order
//	preinstall: <names>      bdeps flagged preinstall, space separated
//	vminstall: <names>       bdeps flagged vminstall
//	runscripts: <names>      bdeps flagged runscripts
//	noinstall: <names>       bdeps flagged noinstall
//	installonly: <names>     bdeps flagged installonly
//
// The flag lines are always written, even when empty (then the line is the
// keyword, one space, nothing), as bs_worker does. bs_worker never writes a
// "dist:" line and has no rpmlist notion of notmeta (it only affects the meta
// digest), so neither does this function.
func renderRPMList(bi *v1.BuildInfo, bdepsDir string) string {
	var lines []string
	for _, b := range bi.Bdeps {
		lines = append(lines, b.Name+" "+filepath.Join(bdepsDir, b.Filename()))
	}
	flagged := func(keyword string, pick func(v1.Bdep) bool) string {
		var names []string
		for _, b := range bi.Bdeps {
			if pick(b) {
				names = append(names, b.Name)
			}
		}
		return keyword + ": " + strings.Join(names, " ")
	}
	lines = append(lines,
		flagged("preinstall", func(b v1.Bdep) bool { return b.Preinstall }),
		flagged("vminstall", func(b v1.Bdep) bool { return b.VMInstall }),
		flagged("runscripts", func(b v1.Bdep) bool { return b.RunScripts }),
		flagged("noinstall", func(b v1.Bdep) bool { return b.NoInstall }),
		flagged("installonly", func(b v1.Bdep) bool { return b.InstallOnly }),
	)
	return strings.Join(lines, "\n") + "\n"
}

// hermeticArgs are the obs-build options of a BuildInfo job that follow the
// sandbox specific (VM or chroot) options. It never contains --repo: obs-build
// must not resolve or download anything.
//
// There is deliberately no --logidlelimit: obs-build has no such option
// (bs_worker enforces it itself). The worker enforces BuildInfo.LogIdleLimit
// by watching the build output, see runWatched.
func hermeticArgs(job *v1.BuildJob, configPath, rpmlistPath, specFile string) []string {
	bi := job.BuildInfo
	granted := int(max(job.Resources.VCPUs, 1))
	jobs := int(bi.Jobs)
	if jobs <= 0 || jobs > granted {
		jobs = granted
	}
	args := []string{
		"--dist", configPath,
		"--rpmlist", rpmlistPath,
		"--arch", bi.ArchPath,
		"--jobs", strconv.Itoa(jobs),
		"--statistics",
		// bs_worker always passes this: mkbaselibs creates the -32bit/-64bit
		// packages (e.g. glibc-devel-32bit from the i586 build), which
		// ExportFilter then routes to the other architecture.
		"--baselibs-internal",
	}
	if bi.Release != "" {
		args = append(args, "--release", bi.Release)
	}
	if bi.DistURL != "" {
		args = append(args, "--disturl", bi.DistURL)
	}
	if bi.Flavor != "" {
		args = append(args, "--buildflavor="+bi.Flavor)
	}
	if bi.ObsPackage != "" {
		args = append(args, "--obspackage="+bi.ObsPackage)
	}
	return append(args, specFile)
}

// prepareHermetic fetches and verifies all bdeps, then writes .build.rpmlist
// and .build.config into the job directory. It returns the paths of both.
func (s *ProcessSandbox) prepareHermetic(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer) (configPath, rpmlistPath string, err error) {
	if err := validateBuildInfo(job); err != nil {
		return "", "", err
	}
	bdepsDir := filepath.Join(jobDir, "bdeps")
	if err := s.fetchBdeps(ctx, job.BuildInfo.Bdeps, bdepsDir, w); err != nil {
		return "", "", err
	}
	rpmlistPath = filepath.Join(jobDir, ".build.rpmlist")
	if err := os.WriteFile(rpmlistPath, []byte(renderRPMList(job.BuildInfo, bdepsDir)), 0o600); err != nil {
		return "", "", fmt.Errorf("writing rpmlist: %w", err)
	}
	configPath = filepath.Join(jobDir, ".build.config")
	if err := os.WriteFile(configPath, []byte(job.BuildInfo.Config), 0o600); err != nil {
		return "", "", fmt.Errorf("writing build config: %w", err)
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: wrote %s (%d bdeps) and %s\n",
		time.Now().Format("15:04:05"), rpmlistPath, len(job.BuildInfo.Bdeps), configPath)
	return configPath, rpmlistPath, nil
}
