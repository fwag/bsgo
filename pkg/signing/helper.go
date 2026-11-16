package signing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Environment of bsgo-sign-helper, set by the signing stage on the rpmsign
// process. The helper has no database access and no ambient credentials: it
// gets the encrypted key blob of the one project it signs for.
const (
	EnvSigndAddr   = "BSGO_SIGND_ADDR"
	EnvSigndCert   = "BSGO_SIGND_TLS_CERT"
	EnvSigndKey    = "BSGO_SIGND_TLS_KEY"
	EnvSigndCA     = "BSGO_SIGND_SERVER_CA"
	EnvProject     = "BSGO_SIGN_PROJECT"
	EnvFlavor      = "BSGO_SIGN_FLAVOR"
	EnvEncKey      = "BSGO_SIGN_ENCKEY"
	EnvCreated     = "BSGO_SIGN_CREATED" // unix time, the RPM build time
	defaultHashAlg = "sha256"
)

// helperArgs is the part of a gpg command line that rpm's default
// %__gpg_sign_cmd uses:
//
//	gpg --no-verbose --no-armor --no-secmem-warning [--digest-algo=H]
//	    [-u NAME] -sbo SIGFILE [-- PLAINTEXT]
type helperArgs struct {
	output, plaintext, hash string
}

// parseHelperArgs reads rpm's gpg arguments. Options that do not change the
// result (verbosity, armor, secmem, key name) are accepted and ignored.
func parseHelperArgs(args []string) (helperArgs, error) {
	a := helperArgs{hash: defaultHashAlg}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return a.finish(args[i+1:])
		}
		if v, ok := strings.CutPrefix(arg, "--digest-algo="); ok {
			a.hash = v
			continue
		}
		if !takesValue(arg) {
			if !ignoredFlag(arg) {
				return a, fmt.Errorf("unsupported argument %q", arg)
			}
			continue
		}
		if i+1 >= len(args) {
			return a, fmt.Errorf("%s needs a value", arg)
		}
		i++
		switch arg {
		case "-sbo", "--output", "-o":
			a.output = args[i]
		case "--digest-algo":
			a.hash = args[i]
		}
	}
	return a, a.validate()
}

// finish takes the arguments after "--": at most one plaintext file.
func (a helperArgs) finish(rest []string) (helperArgs, error) {
	if len(rest) > 1 {
		return a, errors.New("more than one plaintext file")
	}
	if len(rest) == 1 {
		a.plaintext = rest[0]
	}
	return a, a.validate()
}

func takesValue(arg string) bool {
	switch arg {
	case "-sbo", "--output", "-o", "--digest-algo", "-u", "--local-user":
		return true
	}
	return false
}

// ignoredFlag lists the gpg flags rpm passes that do not change the output.
func ignoredFlag(arg string) bool {
	return arg == "gpg" || arg == "-b" || arg == "-s" || strings.HasPrefix(arg, "--no-")
}

func (a helperArgs) validate() error {
	if a.output == "" {
		return errors.New("no output file (-sbo FILE)")
	}
	return nil
}

// staticKeys serves the one key the helper was given.
type staticKeys struct{ key *storage.ProjectSigningKey }

func (s staticKeys) GetProjectKey(_ context.Context, project, flavor, keyType string) (*storage.ProjectSigningKey, error) {
	if project != s.key.ProjectName || flavor != s.key.KeyFlavor || keyType != signer.KeyType {
		return nil, storage.ErrNotFound
	}
	return s.key, nil
}

func (s staticKeys) SaveProjectKey(context.Context, *storage.ProjectSigningKey) error {
	return errors.New("signing helper cannot store keys")
}

// RunHelper is bsgo-sign-helper: a gpg stand-in for rpm's %__gpg_sign_cmd. It
// hashes the plaintext (a file argument or stdin) with the signature trailer
// and has bsgo-signd sign the digest; the plaintext never leaves the host. The
// binary OpenPGP signature packet is written to the output file named by
// -sbo, exactly what rpm reads back from gpg. The signature creation time is
// BSGO_SIGN_CREATED (the RPM build time), so signing is reproducible.
func RunHelper(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader) error {
	a, err := parseHelperArgs(args)
	if err != nil {
		return err
	}
	created, err := strconv.ParseInt(getenv(EnvCreated), 10, 64)
	if err != nil || created <= 0 {
		return fmt.Errorf("%s must be a unix time: %q", EnvCreated, getenv(EnvCreated))
	}
	client, err := newDaemonClient(getenv)
	if err != nil {
		return err
	}
	key := &storage.ProjectSigningKey{
		ProjectName: getenv(EnvProject), KeyFlavor: flavorOf(getenv(EnvFlavor)), KeyType: signer.KeyType,
		EncryptedPrivateKey: getenv(EnvEncKey), Algo: "static",
	}
	if key.ProjectName == "" || key.EncryptedPrivateKey == "" {
		return fmt.Errorf("%s and %s are required", EnvProject, EnvEncKey)
	}
	s := signer.New(client, staticKeys{key}, signer.Config{ExtendBefore: -1, NoAutoCreate: true})

	doc, closeDoc, err := openPlaintext(a.plaintext, stdin)
	if err != nil {
		return err
	}
	defer func() { _ = closeDoc() }()
	sig, err := s.SignStream(ctx, key.ProjectName, key.KeyFlavor, doc, signer.DigestRequest{
		Hash: strings.ToLower(a.hash), SigType: signer.SigTypeBinary, Created: time.Unix(created, 0),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(a.output, sig, 0o600)
}

func flavorOf(f string) string {
	if f == "" {
		return signer.DefaultFlavor
	}
	return f
}

func openPlaintext(path string, stdin io.Reader) (io.Reader, func() error, error) {
	if path == "" || path == "-" {
		return stdin, func() error { return nil }, nil
	}
	f, err := os.Open(path) // #nosec G304 -- path is the plaintext rpm passes to its signing command
	if err != nil {
		return nil, nil, fmt.Errorf("opening plaintext: %w", err)
	}
	return f, f.Close, nil
}

func newDaemonClient(getenv func(string) string) (*signd.Client, error) {
	addr := getenv(EnvSigndAddr)
	if addr == "" {
		return nil, fmt.Errorf("%s is required", EnvSigndAddr)
	}
	cert, keyFile, ca := getenv(EnvSigndCert), getenv(EnvSigndKey), getenv(EnvSigndCA)
	if cert == "" && keyFile == "" && ca == "" {
		return signd.NewClient(addr), nil
	}
	cfg, err := signd.LoadClientTLSConfig(cert, keyFile, ca)
	if err != nil {
		return nil, err
	}
	return signd.NewTLSClient(addr, cfg), nil
}
