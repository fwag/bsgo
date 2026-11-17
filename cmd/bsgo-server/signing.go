package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/signing"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// resolveSignHelper finds bsgo-sign-helper: the configured path, else next to
// this binary, else on PATH. rpm needs an absolute path without blanks.
func resolveSignHelper(configured string) (string, error) {
	path := configured
	if path == "" {
		if self, err := os.Executable(); err == nil {
			cand := filepath.Join(filepath.Dir(self), "bsgo-sign-helper")
			if _, err := os.Stat(cand); err == nil {
				path = cand
			}
		}
	}
	if path == "" {
		found, err := exec.LookPath("bsgo-sign-helper")
		if err != nil {
			return "", fmt.Errorf("bsgo-sign-helper not found (set --sign-helper): %w", err)
		}
		path = found
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(abs, " \t\n'\"\\") {
		return "", fmt.Errorf("sign helper path %q must not contain blanks or quotes", abs)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("sign helper: %w", err)
	}
	return abs, nil
}

// signingPolicy implements jobstatus.Signing: it decides per project whether
// RPMs are signed and sends finished jobs to the signer replicas.
type signingPolicy struct {
	queue signing.Queue
	keys  *signer.Signer
}

// Enabled implements jobstatus.Signing.
func (p signingPolicy) Enabled(ctx context.Context, project string) (bool, error) {
	return p.keys.Enabled(ctx, project, signer.DefaultFlavor)
}

// Request implements jobstatus.Signing.
func (p signingPolicy) Request(ctx context.Context, j signing.Job) error {
	return p.queue.Request(ctx, j)
}

func newSigningQueue(cfg ServerConfig, js jetstream.JetStream) signing.Queue {
	return signing.Queue{
		JS: js, AckWait: cfg.SignAckWait, MaxDeliver: cfg.SignMaxDeliver, Workers: cfg.SignWorkers,
		Logf: func(format string, args ...any) { logf("SIGNER", format, args...) },
	}
}

func runSignerOnly(ctx context.Context, cfg ServerConfig, js jetstream.JetStream, store storage.Store) {
	fmt.Printf("Starting bsgo-server [signer] connected to %s (S3: %s, signd: %s)...\n", cfg.NatsURL, cfg.S3Endpoint, cfg.SigndAddr)
	startSigner(ctx, cfg, js, store)
	<-ctx.Done()
	fmt.Println("\nbsgo-server signer stopped cleanly.")
}

// startSigner runs the signing stage: replicas share the job-finished
// messages through a NATS queue group. Without --sign-rpms the role is idle.
func startSigner(ctx context.Context, cfg ServerConfig, js jetstream.JetStream, store storage.Store) {
	if !cfg.SignRPMs {
		logf("SIGNER", "RPM signing disabled (--sign-rpms=false)")
		return
	}
	keys, err := sharedSigner(cfg, store)
	if err != nil {
		fatalf("SIGNER", "%v", err)
	}
	helper, err := resolveSignHelper(cfg.SignHelper)
	if err != nil {
		fatalf("SIGNER", "%v", err)
	}
	stage := &signing.Stage{
		Keys: keys, Raw: keys, Objects: objectStore(ctx, cfg), States: store, TempRoot: cfg.WorkDir,
		Tool: signing.RPMTool{Helper: helper, Signd: signing.SigndEndpoint{
			Addr: cfg.SigndAddr, TLSCert: cfg.TLSCert, TLSKey: cfg.TLSKey, ServerCA: cfg.ServerCA,
		}},
	}
	queue := newSigningQueue(cfg, js)
	stage.Publish = queue.PublishAnswer
	if err := queue.StartSigner(ctx, stage); err != nil {
		fatalf("SIGNER", "%v", err)
	}
	logf("SIGNER", "signing RPMs with %s through bsgo-signd at %s", helper, cfg.SigndAddr)
}

func fatalf(component, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[%s] [%s] %s\n", time.Now().Format("15:04:05"), component, fmt.Sprintf(format, args...))
	os.Exit(1)
}
