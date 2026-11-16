package signing

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tool is what the stage needs from the rpm tool chain.
type Tool interface {
	// Sign replaces all signatures of the RPM at path with one made by the
	// project key (OBS: sign --delsign -r, sign -r -T buildtime).
	Sign(ctx context.Context, path string, k KeyRef) error
	// Verify checks that every RPM carries a valid signature of the public
	// key (rpm -K against a throw-away rpm database).
	Verify(ctx context.Context, armoredPubKey string, paths []string) error
}

// KeyRef names the key that signs: the project the key belongs to, the
// flavor, the fingerprint (rpm's %_gpg_name) and the encrypted blob that
// bsgo-signd needs.
type KeyRef struct {
	Project, Flavor, Fingerprint, EncryptedKey string
	// Created is the creation time of the key. A signature must not predate
	// its key (rpm -K: "key has been created after the signature"), and a
	// key created on first use is younger than the RPMs it signs first.
	Created time.Time
}

// SigndEndpoint is how bsgo-sign-helper reaches bsgo-signd.
type SigndEndpoint struct {
	Addr, TLSCert, TLSKey, ServerCA string
}

// RPMTool implements Tool with rpm and rpmsign.
type RPMTool struct {
	// Helper is the path of the bsgo-sign-helper binary.
	Helper string
	Signd  SigndEndpoint
}

// run executes a command and returns its combined diagnostics on failure.
func run(cmd *exec.Cmd) (string, error) {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// buildTime returns the RPM's build time, the signature creation time.
func buildTime(ctx context.Context, path string) (time.Time, error) {
	// #nosec G204 -- fixed argv, path follows "--"
	cmd := exec.CommandContext(ctx, "rpm", "-qp", "--nosignature", "--qf", "%{BUILDTIME}", "--", path)
	out, err := run(cmd)
	if err != nil {
		return time.Time{}, fmt.Errorf("rpm -qp %s: %w: %s", filepath.Base(path), err, out)
	}
	sec, err := strconv.ParseInt(out, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, fmt.Errorf("%s has no build time (%q)", filepath.Base(path), out)
	}
	return time.Unix(sec, 0), nil
}

// signCommandEnv is the minimal environment of rpmsign and the helper: no
// ambient credentials of the server process are passed on.
func (t RPMTool) signCommandEnv(k KeyRef, created time.Time, home string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LC_ALL=C",
		EnvSigndAddr + "=" + t.Signd.Addr,
		EnvProject + "=" + k.Project, EnvFlavor + "=" + k.Flavor, EnvEncKey + "=" + k.EncryptedKey,
		EnvCreated + "=" + strconv.FormatInt(created.Unix(), 10),
	}
	if t.Signd.TLSCert != "" {
		env = append(env, EnvSigndCert+"="+t.Signd.TLSCert, EnvSigndKey+"="+t.Signd.TLSKey)
	}
	if t.Signd.ServerCA != "" {
		env = append(env, EnvSigndCA+"="+t.Signd.ServerCA)
	}
	return env
}

// Sign implements Tool. The signature time is the RPM build time (OBS
// -T buildtime), or the creation of the key when that is later, so repeated
// signing gives the same bytes. rpmsign --addsign runs rpm's %__gpg_sign_cmd, which
// is the helper; rpm writes the signature header itself.
func (t RPMTool) Sign(ctx context.Context, path string, k KeyRef) error {
	created, err := buildTime(ctx, path)
	if err != nil {
		return err
	}
	if created.Before(k.Created) {
		created = k.Created
	}
	home, err := os.MkdirTemp("", "bsgo-sign-home-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()
	env := t.signCommandEnv(k, created, home)

	// #nosec G204 -- fixed argv, path follows "--"
	del := exec.CommandContext(ctx, "rpmsign", "--delsign", "--", path)
	del.Env = env
	if out, err := run(del); err != nil {
		return fmt.Errorf("rpmsign --delsign %s: %w: %s", filepath.Base(path), err, out)
	}
	// #nosec G204 -- the helper path and fingerprint are server configuration and key data
	add := exec.CommandContext(ctx, "rpmsign", "--addsign",
		"--define", "__gpg "+t.Helper,
		"--define", "_gpg_name "+k.Fingerprint,
		"--define", "_gpg_digest_algo sha256",
		"--", path)
	add.Env = env
	if out, err := run(add); err != nil {
		return fmt.Errorf("rpmsign --addsign %s: %w: %s", filepath.Base(path), err, out)
	}
	return nil
}

// Verify implements Tool. The key is imported into an empty rpm database, so
// the host's keys cannot make an RPM look valid, and signatures are required
// (_pkgverify_level signature).
func (t RPMTool) Verify(ctx context.Context, armoredPubKey string, paths []string) error {
	dir, err := os.MkdirTemp("", "bsgo-verify-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	keyFile := filepath.Join(dir, "key.asc")
	if err := os.WriteFile(keyFile, []byte(armoredPubKey), 0o600); err != nil {
		return err
	}
	db := filepath.Join(dir, "db")
	// #nosec G204 -- fixed argv
	imp := exec.CommandContext(ctx, "rpm", "--dbpath", db, "--import", keyFile)
	if out, err := run(imp); err != nil {
		return fmt.Errorf("importing project key: %w: %s", err, out)
	}
	for _, p := range paths {
		// #nosec G204 -- fixed argv, path follows "--"
		chk := exec.CommandContext(ctx, "rpm", "--dbpath", db, "--define", "_pkgverify_level signature", "-K", "--", p)
		if out, err := run(chk); err != nil {
			return fmt.Errorf("rpm -K %s: %w: %s", filepath.Base(p), err, out)
		}
	}
	return nil
}
