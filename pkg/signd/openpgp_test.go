package signd_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	_ "crypto/sha256"
	_ "crypto/sha512"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/openSUSE/bsgo/pkg/signd"
)

// newTestEngine returns an engine with small keys so tests stay fast.
func newTestEngine(mk *signd.MasterKey) *signd.SignEngine {
	e := signd.NewSignEngine(mk, nil)
	e.SetKeyParams(signd.KeyParams{ObsName: "example.org", RSABits: 2048})
	return e
}

func testEngine(t *testing.T) *signd.SignEngine {
	t.Helper()
	mk, err := signd.NewMasterKey("openpgp-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mk.Destroy)
	return newTestEngine(mk)
}

func keyring(t *testing.T, armoredPub string) openpgp.EntityList {
	t.Helper()
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armoredPub))
	if err != nil {
		t.Fatalf("reading public key: %v", err)
	}
	return ring
}

func clientDigest(t *testing.T, hashName string, data, trailer []byte) []byte {
	t.Helper()
	h, err := signd.ParseHash(hashName)
	if err != nil {
		t.Fatal(err)
	}
	hs := h.New()
	hs.Write(data)
	hs.Write(trailer)
	return hs.Sum(nil)
}

func verifyDetached(ring openpgp.EntityList, data, sig []byte, armored bool) error {
	if armored {
		_, err := openpgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(data), bytes.NewReader(sig), nil)
		return err
	}
	_, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(data), bytes.NewReader(sig), nil)
	return err
}

func TestGenerateKeyShape(t *testing.T) {
	e := testEngine(t)
	m, err := e.GenerateKey("home:bot", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	if m.Algo != "rsa2048" {
		t.Errorf("algo = %q", m.Algo)
	}
	days := time.Until(m.ExpiresAt).Hours() / 24
	if days < 799 || days > 801 {
		t.Errorf("default expiry %.1f days, want 800", days)
	}
	ring := keyring(t, m.PublicKeyArmored)
	if len(ring) != 1 || len(ring[0].Subkeys) != 0 {
		t.Fatalf("want one primary key without subkeys, got %d entities", len(ring))
	}
	if got := fmt.Sprintf("%X", ring[0].PrimaryKey.Fingerprint); got != m.Fingerprint {
		t.Errorf("fingerprint %s != %s", got, m.Fingerprint)
	}
	if ring[0].PrimaryKey.Version != 4 {
		t.Errorf("key version %d, want 4", ring[0].PrimaryKey.Version)
	}
	if _, ok := ring[0].Identities["home:bot OBS Project <home:bot@example.org>"]; !ok {
		t.Errorf("unexpected user ids: %v", ring[0].Identities)
	}
	if strings.Contains(m.PublicKeyArmored, "PRIVATE") || strings.Contains(m.EncryptedPrivateKey, "PRIVATE") {
		t.Error("private key leaked")
	}
}

func TestGenerateKeyValidation(t *testing.T) {
	e := testEngine(t)
	tests := []struct {
		name    string
		project string
		days    int
	}{
		{"empty project", "", 10},
		{"too many days", "p", 40000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := e.GenerateKey(tt.project, "default", tt.days); err == nil {
				t.Error("expected error")
			}
		})
	}
	m, err := e.GenerateKey("p", "2023-05-10", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keyring(t, m.PublicKeyArmored)[0].Identities["p OBS Project (2023-05-10) <p@example.org>"]; !ok {
		t.Error("flavor missing from user id comment")
	}
}

func TestSignDigestVerifies(t *testing.T) {
	e := testEngine(t)
	m, err := e.GenerateKey("home:bot", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	// rpm 4.20 refuses armor that lacks the newline after the END line.
	if !strings.HasSuffix(m.PublicKeyArmored, "-----END PGP PUBLIC KEY BLOCK-----\n") {
		t.Errorf("armored public key must end with a newline")
	}
	ring := keyring(t, m.PublicKeyArmored)
	created := time.Now().Add(-time.Hour).Truncate(time.Second)
	data := []byte("<repomd>\r\n</repomd>\r\n")

	tests := []struct {
		name    string
		hash    string
		sigType byte
		armored bool
	}{
		{"sha256 binary document armored", "sha256", 0, true},
		{"sha256 binary document raw", "sha256", 0, false},
		{"sha512 raw", "sha512", 0, false},
		{"sha384 armored", "sha384", 0, true},
		{"sha256 text document", "sha256", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := signd.SignParams{Hash: tt.hash, SigType: tt.sigType, Created: created}
			trailer, err := e.SignatureTrailer(m.EncryptedPrivateKey, p, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			digest := clientDigest(t, tt.hash, data, trailer)
			sig, err := e.SignDigest(m.EncryptedPrivateKey, p, digest, tt.armored, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if tt.armored != bytes.HasPrefix(sig, []byte("-----BEGIN PGP SIGNATURE-----")) {
				t.Errorf("armor mismatch: %.40q", sig)
			}
			if tt.armored && !bytes.HasSuffix(sig, []byte("-----END PGP SIGNATURE-----\n")) {
				t.Errorf("armored signature must end with a newline")
			}
			if err := verifyDetached(ring, data, sig, tt.armored); err != nil {
				t.Fatalf("verify: %v", err)
			}
			if err := verifyDetached(ring, append([]byte("x"), data...), sig, tt.armored); err == nil {
				t.Error("tampered data verified")
			}
		})
	}
}

func TestSignDigestRejects(t *testing.T) {
	e := testEngine(t)
	m, err := e.GenerateKey("home:bot", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	good := signd.SignParams{Hash: "sha256", Created: time.Now()}
	digest := make([]byte, 32)
	tests := []struct {
		name   string
		enc    string
		params signd.SignParams
		digest []byte
		now    time.Time
	}{
		{"unknown hash", m.EncryptedPrivateKey, signd.SignParams{Hash: "md5"}, digest, time.Now()},
		{"sha1 refused", m.EncryptedPrivateKey, signd.SignParams{Hash: "sha1"}, digest, time.Now()},
		{"certification sigtype refused", m.EncryptedPrivateKey, signd.SignParams{Hash: "sha256", SigType: 0x13}, digest, time.Now()},
		{"short digest", m.EncryptedPrivateKey, good, digest[:20], time.Now()},
		{"corrupt key", "deadbeef", good, digest, time.Now()},
		{"expired key", m.EncryptedPrivateKey, good, digest, time.Now().AddDate(3, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := e.SignDigest(tt.enc, tt.params, tt.digest, false, tt.now); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestExtendKey(t *testing.T) {
	e := testEngine(t)
	m, err := e.GenerateKey("home:bot", "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	ext, err := e.ExtendKey(m.EncryptedPrivateKey, 800, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ext.Fingerprint != m.Fingerprint {
		t.Error("extension changed the fingerprint")
	}
	if days := time.Until(ext.ExpiresAt).Hours() / 24; days < 799 || days > 801 {
		t.Errorf("extended expiry %.1f days", days)
	}
	if _, err := e.ExtendKey(m.EncryptedPrivateKey, 0, time.Now()); err == nil {
		t.Error("zero days accepted")
	}

	// An expired key can be extended and then signs again.
	later := time.Now().AddDate(0, 0, 30)
	revived, err := e.ExtendKey(m.EncryptedPrivateKey, 100, later)
	if err != nil {
		t.Fatal(err)
	}
	p := signd.SignParams{Hash: "sha256", Created: later}
	trailer, err := e.SignatureTrailer(revived.EncryptedPrivateKey, p, later)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("after extension")
	sig, err := e.SignDigest(revived.EncryptedPrivateKey, p, clientDigest(t, "sha256", data, trailer), false, later)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packet.Config{Time: func() time.Time { return later.Add(time.Minute) }}
	_, err = openpgp.CheckDetachedSignature(keyring(t, revived.PublicKeyArmored), bytes.NewReader(data), bytes.NewReader(sig), cfg)
	if err != nil {
		t.Fatalf("verify with extended key: %v", err)
	}
}

func TestPublicKeyMatchesGenerate(t *testing.T) {
	e := testEngine(t)
	m, err := e.GenerateKey("home:bot", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := e.PublicKey(m.EncryptedPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if pk.Fingerprint != m.Fingerprint || pk.PublicKeyArmored != m.PublicKeyArmored || !pk.ExpiresAt.Equal(m.ExpiresAt) {
		t.Error("PublicKey differs from GenerateKey")
	}
}

func startServer(t *testing.T, e *signd.SignEngine) string {
	t.Helper()
	srv := signd.NewServer(e)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestProtocolEndToEnd(t *testing.T) {
	ctx := context.Background()
	c := signd.NewClient(startServer(t, testEngine(t)))

	m, err := c.GenerateKey(ctx, "demo", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := c.PublicKey(ctx, m.EncryptedPrivateKey)
	if err != nil || pk.Fingerprint != m.Fingerprint {
		t.Fatalf("PUBKEY: %v %+v", err, pk)
	}
	ext, err := c.ExtendKey(ctx, m.EncryptedPrivateKey, 900)
	if err != nil || ext.Fingerprint != m.Fingerprint || !ext.ExpiresAt.After(m.ExpiresAt) {
		t.Fatalf("EXTEND: %v %+v", err, ext)
	}

	data := []byte("protocol payload")
	p := signd.SignParams{Hash: "sha256", Created: time.Now().Truncate(time.Second)}
	trailer, err := c.SignatureTrailer(ctx, ext.EncryptedPrivateKey, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, armored := range []bool{false, true} {
		sig, err := c.SignDigest(ctx, ext.EncryptedPrivateKey, p, clientDigest(t, "sha256", data, trailer), armored)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyDetached(keyring(t, ext.PublicKeyArmored), data, sig, armored); err != nil {
			t.Fatalf("armored=%v: %v", armored, err)
		}
	}
}

func TestProtocolErrors(t *testing.T) {
	addr := startServer(t, testEngine(t))
	tests := []struct {
		name string
		line string
		want string
	}{
		{"unknown command", "FROB x", "ERROR: unknown command"},
		{"legacy raw command", "SIGN_DIGEST aa bb", "ERROR: unknown command"},
		{"missing args", "SIGN abcd sha256", "ERROR: missing arguments"},
		{"genkey without project", "GENKEY", "ERROR: missing arguments"},
		{"bad days", "GENKEY p default abc", "ERROR: genkey failed"},
		{"bad hex digest", "SIGN aa sha256 zz 0 1 binary", "ERROR: sign failed"},
		{"bad output format", "SIGN aa sha256 00 0 1 pem", "ERROR: sign failed"},
		{"bad key blob", "PUBKEY nothex", "ERROR: pubkey failed"},
		{"ping", "PING", "PONG"},
		{"quit", "QUIT", "BYE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := fmt.Fprintf(conn, "%s\n", tt.line); err != nil {
				t.Fatal(err)
			}
			resp, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(resp, tt.want) {
				t.Errorf("response %q, want prefix %q", resp, tt.want)
			}
		})
	}
}

func TestParseHash(t *testing.T) {
	for name, want := range map[string]crypto.Hash{"sha256": crypto.SHA256, "SHA512": crypto.SHA512} {
		got, err := signd.ParseHash(name)
		if err != nil || got != want {
			t.Errorf("ParseHash(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := signd.ParseHash("md5"); err == nil {
		t.Error("md5 accepted")
	}
}
