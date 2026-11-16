package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// newSigndClient connects to bsgo-signd, with mTLS when a client certificate
// is configured. It dials per request, so signd may start later.
func newSigndClient(cfg ServerConfig) (*signd.Client, error) {
	if cfg.TLSCert == "" {
		return signd.NewClient(cfg.SigndAddr), nil
	}
	tlsCfg, err := signd.LoadClientTLSConfig(cfg.TLSCert, cfg.TLSKey, cfg.ServerCA)
	if err != nil {
		return nil, fmt.Errorf("loading signd client TLS config: %w", err)
	}
	return signd.NewTLSClient(cfg.SigndAddr, tlsCfg), nil
}

var (
	signerOnce   sync.Once
	signerShared *signer.Signer
	signerErr    error
)

// sharedSigner returns the one signd client and key resolver of the process:
// the publisher (repository metadata) and the signing stage (RPMs) use the
// same instance, so key creation and extension are serialized together.
func sharedSigner(cfg ServerConfig, store storage.Store) (*signer.Signer, error) {
	signerOnce.Do(func() {
		client, err := newSigndClient(cfg)
		if err != nil {
			signerErr = err
			return
		}
		signerShared = signer.New(client, store, signer.Config{NoAutoCreate: !cfg.SignCreateKeys})
	})
	return signerShared, signerErr
}

// newMetadataSigner returns the publisher's repository metadata signer, or
// nil (unsigned repositories) when signing is disabled.
func newMetadataSigner(cfg ServerConfig, store storage.Store) publisher.MetadataSigner {
	if !cfg.SignRepos {
		logf("PUBLISHER", "repository signing disabled (--sign-repos=false): publishing unsigned repositories")
		return nil
	}
	keys, err := sharedSigner(cfg, store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[PUBLISHER] %v\n", err)
		os.Exit(1)
	}
	return signer.NewRepoSigner(keys, func(format string, args ...any) {
		logf("PUBLISHER", format, args...)
	})
}
