package signd

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
)

// Server implements the bare-metal signing daemon protocol.
type Server struct {
	engine   *SignEngine
	listener net.Listener
	mu       sync.Mutex
	closed   bool
}

func NewServer(engine *SignEngine) *Server {
	return &Server{
		engine: engine,
	}
}

// Start begins listening on the specified network address (e.g. "0.0.0.0:5167").
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind signing daemon to %s: %w", addr, err)
	}

	s.mu.Lock()
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

		go s.handleConnection(conn)
	}
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

		parts := strings.Split(line, " ")
		cmd := strings.ToUpper(parts[0])

		switch cmd {
		case "SIGN_DIGEST":
			// Protocol: SIGN_DIGEST <encrypted_priv_key_hex> <digest_hex>
			if len(parts) < 3 {
				_, _ = io.WriteString(conn, "ERROR: missing arguments\n")
				continue
			}
			encKey := parts[1]
			digest, err := hex.DecodeString(parts[2])
			if err != nil {
				_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: invalid digest hex: %v\n", err))
				continue
			}

			sig, err := s.engine.SignDigestWithProjectKey(encKey, digest)
			if err != nil {
				_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: signing failed: %v\n", err))
				continue
			}

			_, _ = io.WriteString(conn, fmt.Sprintf("OK %s\n", hex.EncodeToString(sig)))

		case "PING":
			_, _ = io.WriteString(conn, "PONG bsgo-signd ready\n")

		case "QUIT":
			_, _ = io.WriteString(conn, "BYE\n")
			return

		default:
			_, _ = io.WriteString(conn, fmt.Sprintf("ERROR: unknown command %q\n", cmd))
		}
	}
}

// Client provides a helper to communicate with the bsgo-signd server over TCP/mTLS.
type Client struct {
	addr string
}

func NewClient(addr string) *Client {
	return &Client{addr: addr}
}

func (c *Client) SignDigest(ctx context.Context, encryptedPrivKeyHex string, digest []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr)
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
