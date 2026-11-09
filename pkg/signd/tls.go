package signd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
)

// LoadServerTLSConfig loads server certificates and optionally client CA for mTLS.
func LoadServerTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, nil
	}

	cleanCert := filepath.Clean(certFile)
	cleanKey := filepath.Clean(keyFile)

	cert, err := tls.LoadX509KeyPair(cleanCert, cleanKey)
	if err != nil {
		return nil, fmt.Errorf("failed loading server certificate keypair: %w", err)
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if clientCAFile != "" {
		cleanCA := filepath.Clean(clientCAFile)
		caData, err := os.ReadFile(cleanCA)
		if err != nil {
			return nil, fmt.Errorf("failed reading client CA file: %w", err)
		}
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caData) {
			return nil, fmt.Errorf("failed appending client CA certificates")
		}
		cfg.ClientCAs = caPool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return cfg, nil
}

// LoadClientTLSConfig loads client certificates (mTLS) and/or server root CA for verification.
func LoadClientTLSConfig(certFile, keyFile, serverCAFile string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	if certFile != "" && keyFile != "" {
		cleanCert := filepath.Clean(certFile)
		cleanKey := filepath.Clean(keyFile)
		cert, err := tls.LoadX509KeyPair(cleanCert, cleanKey)
		if err != nil {
			return nil, fmt.Errorf("failed loading client certificate keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	if serverCAFile != "" {
		cleanCA := filepath.Clean(serverCAFile)
		caData, err := os.ReadFile(cleanCA)
		if err != nil {
			return nil, fmt.Errorf("failed reading server CA file: %w", err)
		}
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caData) {
			return nil, fmt.Errorf("failed appending server CA certificates")
		}
		cfg.RootCAs = caPool
	}

	return cfg, nil
}
