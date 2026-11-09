package signd

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// Server implements the bare-metal signing daemon protocol.
type Server struct {
	engine      *SignEngine
	listener    net.Listener
	tlsConfig   *tls.Config
	allowedNets []*net.IPNet
	mu          sync.Mutex
	closed      bool
}

func NewServer(engine *SignEngine) *Server {
	return &Server{
		engine: engine,
	}
}

// SetTLSConfig configures TLS / mTLS for the server listener.
func (s *Server) SetTLSConfig(cfg *tls.Config) {
	s.mu.Lock()
	s.tlsConfig = cfg
	s.mu.Unlock()
}

// SetAllowedCIDRs configures allowed client IP addresses or subnets.
func (s *Server) SetAllowedCIDRs(cidrs []string) error {
	var nets []*net.IPNet
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		target := raw
		if !strings.Contains(target, "/") {
			ip := net.ParseIP(target)
			if ip == nil {
				return fmt.Errorf("invalid IP address: %s", raw)
			}
			if ip.To4() != nil {
				target += "/32"
			} else {
				target += "/128"
			}
		}
		_, ipNet, err := net.ParseCIDR(target)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", raw, err)
		}
		nets = append(nets, ipNet)
	}
	s.mu.Lock()
	s.allowedNets = nets
	s.mu.Unlock()
	return nil
}

func (s *Server) isAllowed(addr net.Addr) bool {
	s.mu.Lock()
	allowed := s.allowedNets
	s.mu.Unlock()

	if len(allowed) == 0 {
		return true
	}

	ip := extractIP(addr)
	if ip == nil {
		return false
	}

	for _, n := range allowed {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func extractIP(addr net.Addr) net.IP {
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.IP
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return net.ParseIP(addr.String())
	}
	return net.ParseIP(host)
}

// Serve begins accepting incoming connections on the listener.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.tlsConfig != nil {
		ln = tls.NewListener(ln, s.tlsConfig)
	}
	s.listener = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			continue
		}

		if !s.isAllowed(conn.RemoteAddr()) {
			fmt.Printf("[%s] [signd] Rejected connection from unauthorized IP: %s\n",
				time.Now().Format("15:04:05"), conn.RemoteAddr().String())
			_ = conn.Close()
			continue
		}

		go s.handleConnection(conn)
	}
}

// Start begins listening on the specified network address (e.g. "0.0.0.0:5167").
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind signing daemon to %s: %w", addr, err)
	}
	return s.Serve(ln)
}

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()
	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if s.dispatchCommand(conn, line, remoteAddr) {
			return
		}
	}
}

func (s *Server) dispatchCommand(conn io.Writer, line, remoteAddr string) bool {
	parts := strings.Split(line, " ")
	cmd := strings.ToUpper(parts[0])

	switch cmd {
	case "SIGN_DIGEST":
		s.handleSignDigest(conn, parts, remoteAddr)
	case "GEN_KEY":
		s.handleGenKey(conn, parts, remoteAddr)
	case "PING":
		fmt.Printf("[%s] [signd] Responded to PING from %s\n", time.Now().Format("15:04:05"), remoteAddr)
		_, _ = io.WriteString(conn, "PONG bsgo-signd ready\n")
	case "QUIT":
		_, _ = io.WriteString(conn, "BYE\n")
		return true
	default:
		fmt.Printf("[%s] [signd] Unknown command %q from %s\n", time.Now().Format("15:04:05"), cmd, remoteAddr)
		_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: unknown command %q\n", cmd))
	}
	return false
}

func (s *Server) handleSignDigest(conn io.Writer, parts []string, remoteAddr string) {
	if len(parts) < 3 {
		_, _ = io.WriteString(conn, "ERROR: missing arguments\n")
		return
	}
	encKey := parts[1]
	digest, err := hex.DecodeString(parts[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [signd] Invalid digest hex from %s: %v\n", time.Now().Format("15:04:05"), remoteAddr, err)
		_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: invalid digest hex: %v\n", err))
		return
	}

	sig, err := s.engine.SignDigestWithProjectKey(encKey, digest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [signd] Signing failed for client %s: %v\n", time.Now().Format("15:04:05"), remoteAddr, err)
		_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: signing failed: %v\n", err))
		return
	}

	dPreview := parts[2]
	if len(dPreview) > 12 {
		dPreview = dPreview[:12]
	}
	fmt.Printf("[%s] [signd] Successfully signed digest %s... for client %s\n",
		time.Now().Format("15:04:05"), dPreview, remoteAddr)

	_, _ = io.WriteString(conn, fmt.Sprintf("OK %s\n", hex.EncodeToString(sig)))
}

func (s *Server) handleGenKey(conn io.Writer, parts []string, remoteAddr string) {
	if len(parts) < 2 {
		_, _ = io.WriteString(conn, "ERROR: missing project argument\n")
		return
	}
	project := parts[1]
	flavor := "default"
	if len(parts) >= 3 && parts[2] != "" {
		flavor = parts[2]
	}
	keyType := "gpg"
	if len(parts) >= 4 && parts[3] != "" {
		keyType = parts[3]
	}

	keyModel, err := s.engine.GenerateProjectKeypair(project, flavor, keyType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [signd] Failed generating keypair for %s: %v\n",
			time.Now().Format("15:04:05"), project, err)
		_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: key generation failed: %v\n", err))
		return
	}

	fmt.Printf("[%s] [signd] Generated new RSA-2048 keypair for project: %s (flavor: %s, type: %s, fp: %s) requested by %s\n",
		time.Now().Format("15:04:05"), project, flavor, keyType, keyModel.Fingerprint, remoteAddr)

	pubHex := hex.EncodeToString([]byte(keyModel.PublicKeyArmored))
	_, _ = io.WriteString(conn, fmt.Sprintf("OK %s %s %s\n", pubHex, keyModel.EncryptedPrivateKey, keyModel.Fingerprint))
}

// Client provides a helper to communicate with the bsgo-signd server over TCP/mTLS.
type Client struct {
	addr      string
	tlsConfig *tls.Config
}

func NewClient(addr string) *Client {
	return &Client{addr: addr}
}

func NewTLSClient(addr string, tlsConfig *tls.Config) *Client {
	return &Client{addr: addr, tlsConfig: tlsConfig}
}

func (c *Client) SetTLSConfig(cfg *tls.Config) {
	c.tlsConfig = cfg
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	if c.tlsConfig != nil {
		tlsDialer := &tls.Dialer{NetDialer: &d, Config: c.tlsConfig}
		return tlsDialer.DialContext(ctx, "tcp", c.addr)
	}
	return d.DialContext(ctx, "tcp", c.addr)
}

func (c *Client) SignDigest(ctx context.Context, encryptedPrivKeyHex string, digest []byte) ([]byte, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed connecting to bsgo-signd at %s: %w", c.addr, err)
	}
	defer conn.Close()

	cmd := fmt.Sprintf("SIGN_DIGEST %s %s\n", encryptedPrivKeyHex, hex.EncodeToString(digest))
	if _, err := io.WriteString(conn, cmd); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	resp = strings.TrimSpace(resp)
	if !strings.HasPrefix(resp, "OK ") {
		return nil, fmt.Errorf("signer daemon rejected request: %s", resp)
	}

	sigHex := strings.TrimPrefix(resp, "OK ")
	return hex.DecodeString(sigHex)
}

func (c *Client) GenerateKey(ctx context.Context, project, flavor, keyType string) (*storage.ProjectSigningKey, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed connecting to bsgo-signd at %s: %w", c.addr, err)
	}
	defer conn.Close()

	cmd := fmt.Sprintf("GEN_KEY %s %s %s\n", project, flavor, keyType)
	if _, err := io.WriteString(conn, cmd); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	resp = strings.TrimSpace(resp)
	if !strings.HasPrefix(resp, "OK ") {
		return nil, fmt.Errorf("signer daemon rejected keygen: %s", resp)
	}

	parts := strings.Split(strings.TrimPrefix(resp, "OK "), " ")
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected keygen response from signd: %s", resp)
	}

	pubBytes, err := hex.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid public key hex: %w", err)
	}

	return &storage.ProjectSigningKey{
		ProjectName:         project,
		KeyFlavor:           flavor,
		KeyType:             keyType,
		PublicKeyArmored:    string(pubBytes),
		EncryptedPrivateKey: parts[1],
		Fingerprint:         parts[2],
	}, nil
}
