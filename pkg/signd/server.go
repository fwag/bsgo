package signd

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
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

		if s.dispatchCommand(conn, line) {
			return
		}
	}
}

func (s *Server) dispatchCommand(conn io.Writer, line string) bool {
	parts := strings.Split(line, " ")
	cmd := strings.ToUpper(parts[0])

	switch cmd {
	case "SIGN_DIGEST":
		s.handleSignDigest(conn, parts)
	case "PING":
		_, _ = io.WriteString(conn, "PONG bsgo-signd ready\n")
	case "QUIT":
		_, _ = io.WriteString(conn, "BYE\n")
		return true
	default:
		_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: unknown command %q\n", cmd))
	}
	return false
}

func (s *Server) handleSignDigest(conn io.Writer, parts []string) {
	if len(parts) < 3 {
		_, _ = io.WriteString(conn, "ERROR: missing arguments\n")
		return
	}
	encKey := parts[1]
	digest, err := hex.DecodeString(parts[2])
	if err != nil {
		_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: invalid digest hex: %v\n", err))
		return
	}

	sig, err := s.engine.SignDigestWithProjectKey(encKey, digest)
	if err != nil {
		_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: signing failed: %v\n", err))
		return
	}

	_, _ = io.WriteString(conn, fmt.Sprintf("OK %s\n", hex.EncodeToString(sig)))
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

func (c *Client) SignDigest(ctx context.Context, encryptedPrivKeyHex string, digest []byte) ([]byte, error) {
	var d net.Dialer
	var conn net.Conn
	var err error

	if c.tlsConfig != nil {
		tlsDialer := &tls.Dialer{NetDialer: &d, Config: c.tlsConfig}
		conn, err = tlsDialer.DialContext(ctx, "tcp", c.addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", c.addr)
	}
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
