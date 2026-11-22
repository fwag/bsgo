package worker

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Placeholders usable in sandbox.nspawn_args entries.
const (
	// placeholderHostname expands to the host name as `hostname` prints it.
	placeholderHostname = "{hostname}"
	// placeholderProcFilesystems expands to the path of the per-worker copy of
	// /proc/filesystems without selinuxfs.
	placeholderProcFilesystems = "{proc_filesystems}"
)

// procFilesystemsName is the file name of the filtered /proc/filesystems copy
// in the worker work directory.
const procFilesystemsName = "proc-filesystems"

// DefaultNspawnArgs are the systemd-nspawn options used when sandbox.nspawn_args
// is not configured. A configured list (even an empty one) replaces them
// entirely, so each default can be dropped individually; the placeholders
// {hostname} and {proc_filesystems} are available to configured entries.
//
//   - --hostname: init_buildsystem writes the host's `hostname` into the build
//     root's /etc/hosts, but nspawn would name the container after the machine
//     (obsbuild.<root>); tests resolving their own hostname (cups) fail.
//   - CAP_IPC_LOCK and RLIMIT_MEMLOCK=infinity: libgcrypt secure memory
//     (mlock) as in a KVM guest.
//
// Not included: binding a filtered /proc/filesystems (to hide the host's
// selinuxfs from SELinux-aware test suites such as coreutils). systemd-nspawn
// sets up binds before it mounts /proc in the container, so the bind fails
// with "Failed to clone ...: No such file or directory" and every build dies.
// The {proc_filesystems} placeholder stays available for experiments.
func DefaultNspawnArgs() []string {
	return []string{
		"--hostname=" + placeholderHostname,
		"--capability=CAP_IPC_LOCK",
		"--rlimit=RLIMIT_MEMLOCK=infinity",
	}
}

// nspawnEnv carries the values of the placeholders.
type nspawnEnv struct {
	hostname        string
	procFilesystems string
}

// expandNspawnArgs substitutes the placeholders in tmpl; it never modifies it.
func expandNspawnArgs(tmpl []string, env nspawnEnv) []string {
	r := strings.NewReplacer(
		placeholderHostname, env.hostname,
		placeholderProcFilesystems, env.procFilesystems,
	)
	out := make([]string, len(tmpl))
	for i, a := range tmpl {
		out[i] = r.Replace(a)
	}
	return out
}

// usesPlaceholder reports whether any entry of args contains p.
func usesPlaceholder(args []string, p string) bool {
	for _, a := range args {
		if strings.Contains(a, p) {
			return true
		}
	}
	return false
}

// filterProcFilesystems drops the selinuxfs line from the content of
// /proc/filesystems.
func filterProcFilesystems(data []byte) []byte {
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) > 0 && f[len(f)-1] == "selinuxfs" {
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

// PrepareNspawnArgs resolves the systemd-nspawn arguments of the worker:
// configured replaces DefaultNspawnArgs when non-nil. When an argument uses
// {proc_filesystems}, the filtered /proc/filesystems copy is written into
// workDir (once per worker start).
func PrepareNspawnArgs(configured []string, workDir string) ([]string, error) {
	tmpl := configured
	if tmpl == nil {
		tmpl = DefaultNspawnArgs()
	}
	var env nspawnEnv
	if usesPlaceholder(tmpl, placeholderHostname) {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("nspawn hostname: %w", err)
		}
		env.hostname = h
	}
	if usesPlaceholder(tmpl, placeholderProcFilesystems) {
		p, err := writeProcFilesystems(workDir)
		if err != nil {
			return nil, err
		}
		env.procFilesystems = p
	}
	return expandNspawnArgs(tmpl, env), nil
}

// writeProcFilesystems writes the filtered copy of /proc/filesystems and
// returns its absolute path.
func writeProcFilesystems(workDir string) (string, error) {
	data, err := os.ReadFile("/proc/filesystems")
	if err != nil {
		return "", fmt.Errorf("read /proc/filesystems: %w", err)
	}
	dir, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, procFilesystemsName)
	// The container is started by root (build-sudo), which reads the file
	// regardless of its mode.
	if err := os.WriteFile(path, filterProcFilesystems(data), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// shellQuoteArgs joins args into one string that obs-build's
// `eval "VM_ARGS=($ARG)"` splits back into exactly args.
func shellQuoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// nspawnVMArgs returns the configured nspawn arguments plus a bind mount of
// the build root's own /tmp onto /tmp. systemd-nspawn otherwise mounts a
// tmpfs there that is limited to 10% of the host memory (3 GiB on a 32 GiB
// host) and fills up when a large Go or C++ build compiles through /tmp
// ("No space left on device"). OBS builds in a chroot or a KVM disk, where
// /tmp lives on the build root, and the bound directory is wiped with the
// build root. A configured argument that already mounts /tmp (a bind or a
// tmpfs with another size) wins.
func nspawnVMArgs(configured []string, buildRoot string) []string {
	out := append([]string(nil), configured...)
	if mountsTmp(configured) {
		return out
	}
	src := strings.ReplaceAll(filepath.Join(buildRoot, "tmp"), ":", `\:`)
	return append(out, "--bind="+src+":/tmp")
}

// mountsTmp reports whether an nspawn argument sets up the container's /tmp.
func mountsTmp(args []string) bool {
	for _, a := range args {
		if a == "--tmpfs=/tmp" || strings.HasPrefix(a, "--tmpfs=/tmp:") {
			return true
		}
		for _, opt := range []string{"--bind=", "--bind-ro=", "--overlay=", "--overlay-ro="} {
			v, ok := strings.CutPrefix(a, opt)
			if ok && (strings.HasSuffix(v, ":/tmp") || strings.Contains(v, ":/tmp:")) {
				return true
			}
		}
	}
	return false
}
