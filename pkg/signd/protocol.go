package signd

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxLine bounds one protocol line; an encrypted RSA-4096 key is about 6 KiB.
const maxLine = 1 << 20

// handler executes one protocol command and returns the response fields.
type handler struct {
	minArgs int
	usage   string
	run     func(e *SignEngine, args []string) ([]string, error)
}

var handlers = map[string]handler{
	"GENKEY":  {1, "GENKEY <project> [flavor] [days]", runGenKey},
	"PUBKEY":  {1, "PUBKEY <enckey>", runPubKey},
	"EXTEND":  {2, "EXTEND <enckey> <days>", runExtend},
	"TRAILER": {4, "TRAILER <enckey> <hashalgo> <sigtype> <created-unix>", runTrailer},
	"SIGN":    {6, "SIGN <enckey> <hashalgo> <hexdigest> <sigtype> <created-unix> <armor|binary>", runSign},
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if s.dispatchCommand(conn, line, remoteAddr) {
			return
		}
	}
}

func logf(format string, args ...any) {
	fmt.Printf("[%s] [signd] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func logErrf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[%s] [signd] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (s *Server) dispatchCommand(conn io.Writer, line, remoteAddr string) bool {
	parts := strings.Fields(line)
	cmd := strings.ToUpper(parts[0])

	switch cmd {
	case "PING":
		logf("Responded to PING from %s", remoteAddr)
		_, _ = io.WriteString(conn, "PONG bsgo-signd ready\n")
		return false
	case "QUIT":
		_, _ = io.WriteString(conn, "BYE\n")
		return true
	}

	h, ok := handlers[cmd]
	if !ok {
		logErrf("Unknown command %q from %s", cmd, remoteAddr)
		_, _ = fmt.Fprintf(conn, "ERROR: unknown command %q\n", cmd)
		return false
	}
	if len(parts)-1 < h.minArgs {
		_, _ = fmt.Fprintf(conn, "ERROR: missing arguments, usage: %s\n", h.usage)
		return false
	}
	fields, err := h.run(s.engine, parts[1:])
	if err != nil {
		logErrf("%s failed for client %s: %v", cmd, remoteAddr, err)
		_, _ = fmt.Fprintf(conn, "ERROR: %s failed: %v\n", strings.ToLower(cmd), err)
		return false
	}
	logf("%s succeeded for client %s", cmd, remoteAddr)
	_, _ = io.WriteString(conn, "OK "+strings.Join(fields, " ")+"\n")
	return false
}

// materialFields encodes key material as response fields: armored public key
// (hex), encrypted private key, fingerprint, algorithm, expiry unix (0 none).
func materialFields(m *KeyMaterial) []string {
	var exp int64
	if !m.ExpiresAt.IsZero() {
		exp = m.ExpiresAt.Unix()
	}
	return []string{
		hex.EncodeToString([]byte(m.PublicKeyArmored)),
		m.EncryptedPrivateKey,
		m.Fingerprint,
		m.Algo,
		strconv.FormatInt(exp, 10),
	}
}

func parseMaterial(fields []string) (*KeyMaterial, error) {
	if len(fields) != 5 {
		return nil, fmt.Errorf("unexpected key response with %d fields", len(fields))
	}
	pub, err := hex.DecodeString(fields[0])
	if err != nil {
		return nil, fmt.Errorf("invalid public key hex: %w", err)
	}
	exp, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid expiry: %w", err)
	}
	m := &KeyMaterial{
		PublicKeyArmored:    string(pub),
		EncryptedPrivateKey: fields[1],
		Fingerprint:         fields[2],
		Algo:                fields[3],
	}
	if exp != 0 {
		m.ExpiresAt = time.Unix(exp, 0).UTC()
	}
	return m, nil
}

func runGenKey(e *SignEngine, args []string) ([]string, error) {
	flavor, days := "default", 0
	if len(args) >= 2 && args[1] != "" {
		flavor = args[1]
	}
	if len(args) >= 3 {
		d, err := strconv.Atoi(args[2])
		if err != nil {
			return nil, fmt.Errorf("invalid days %q: %w", args[2], err)
		}
		days = d
	}
	m, err := e.GenerateKey(args[0], flavor, days)
	if err != nil {
		return nil, err
	}
	logf("Generated %s key for project %s (flavor %s, fingerprint %s, expires %s)",
		m.Algo, args[0], flavor, m.Fingerprint, m.ExpiresAt.Format(time.RFC3339))
	return materialFields(m), nil
}

func runPubKey(e *SignEngine, args []string) ([]string, error) {
	m, err := e.PublicKey(args[0])
	if err != nil {
		return nil, err
	}
	return materialFields(m), nil
}

func runExtend(e *SignEngine, args []string) ([]string, error) {
	days, err := strconv.Atoi(args[1])
	if err != nil {
		return nil, fmt.Errorf("invalid days %q: %w", args[1], err)
	}
	m, err := e.ExtendKey(args[0], days, time.Now())
	if err != nil {
		return nil, err
	}
	logf("Extended key %s to %s", m.Fingerprint, m.ExpiresAt.Format(time.RFC3339))
	return materialFields(m), nil
}

func parseSignParams(hashAlgo, sigType, created string) (SignParams, error) {
	st, err := strconv.ParseUint(sigType, 10, 8)
	if err != nil {
		return SignParams{}, fmt.Errorf("invalid signature type %q", sigType)
	}
	ts, err := strconv.ParseInt(created, 10, 64)
	if err != nil || ts < 0 {
		return SignParams{}, fmt.Errorf("invalid creation time %q", created)
	}
	return SignParams{Hash: hashAlgo, SigType: byte(st), Created: time.Unix(ts, 0).UTC()}, nil
}

func runTrailer(e *SignEngine, args []string) ([]string, error) {
	p, err := parseSignParams(args[1], args[2], args[3])
	if err != nil {
		return nil, err
	}
	trailer, err := e.SignatureTrailer(args[0], p, time.Now())
	if err != nil {
		return nil, err
	}
	return []string{hex.EncodeToString(trailer)}, nil
}

func runSign(e *SignEngine, args []string) ([]string, error) {
	p, err := parseSignParams(args[1], args[3], args[4])
	if err != nil {
		return nil, err
	}
	digest, err := hex.DecodeString(args[2])
	if err != nil {
		return nil, fmt.Errorf("invalid digest hex: %w", err)
	}
	var armored bool
	switch args[5] {
	case "armor":
		armored = true
	case "binary":
	default:
		return nil, fmt.Errorf("output format must be armor or binary, got %q", args[5])
	}
	sig, err := e.SignDigest(args[0], p, digest, armored, time.Now())
	if err != nil {
		return nil, err
	}
	return []string{hex.EncodeToString(sig)}, nil
}

var errEmptyResponse = errors.New("empty response from signd")
