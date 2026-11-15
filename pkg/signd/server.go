package signd

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
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
