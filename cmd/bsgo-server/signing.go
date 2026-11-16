package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/signd"
	"github.com/openSUSE/bsgo/pkg/signer"
	"github.com/openSUSE/bsgo/pkg/signing"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// newSigner returns the key resolver of the server: it talks to bsgo-signd and
// keeps the project keys in the store.
func newSigner(cfg ServerConfig, store storage.Store) (*signer.Signer, error) {
	client := signd.NewClient(cfg.SigndAddr)
	if cfg.TLSCert != "" || cfg.TLSKey != "" || cfg.ServerCA != "" {
		tlsCfg, err := signd.LoadClientTLSConfig(cfg.TLSCert, cfg.TLSKey, cfg.ServerCA)
		if err != nil {
			return nil, fmt.Errorf("signd TLS configuration: %w", err)
		}
		client.SetTLSConfig(tlsCfg)
	}
	return signer.New(client, store, signer.Config{NoAutoCreate: !cfg.SignAutoCreate}), nil
}

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
	nc   *nats.Conn
	keys *signer.Signer
}

// Enabled implements jobstatus.Signing.
func (p signingPolicy) Enabled(ctx context.Context, project string) (bool, error) {
	return p.keys.Enabled(ctx, project, signer.DefaultFlavor)
}

// Request implements jobstatus.Signing.
func (p signingPolicy) Request(_ context.Context, j signing.Job) error {
	data, err := signing.Encode(j)
	if err != nil {
		return err
	}
	return p.nc.Publish(natsqueue.SubjectJobFinished, data)
}

func runSignerOnly(ctx context.Context, cfg ServerConfig, nc *nats.Conn, store storage.Store) {
	fmt.Printf("Starting bsgo-server [signer] connected to %s (S3: %s, signd: %s)...\n", cfg.NatsURL, cfg.S3Endpoint, cfg.SigndAddr)
	startSigner(ctx, cfg, nc, store)
	<-ctx.Done()
	fmt.Println("\nbsgo-server signer stopped cleanly.")
}

// startSigner runs the signing stage: replicas share the job-finished
// messages through a NATS queue group. Without --sign-rpms the role is idle.
func startSigner(ctx context.Context, cfg ServerConfig, nc *nats.Conn, store storage.Store) {
	if !cfg.SignRPMs {
		logf("SIGNER", "RPM signing disabled (--sign-rpms=false)")
		return
	}
	keys, err := newSigner(cfg, store)
	if err != nil {
		fatalf("SIGNER", "%v", err)
	}
	helper, err := resolveSignHelper(cfg.SignHelper)
	if err != nil {
		fatalf("SIGNER", "%v", err)
	}
	stage := &signing.Stage{
		Keys: keys, Objects: objectStore(ctx, cfg), States: store, TempRoot: cfg.WorkDir,
		Tool: signing.RPMTool{Helper: helper, Signd: signing.SigndEndpoint{
			Addr: cfg.SigndAddr, TLSCert: cfg.TLSCert, TLSKey: cfg.TLSKey, ServerCA: cfg.ServerCA,
		}},
		Publish: func(_ context.Context, data []byte) error { return nc.Publish(natsqueue.SubjectJobSigned, data) },
	}
	subscribeJobFinishedForSigner(ctx, nc, stage, cfg.SignWorkers)
	logf("SIGNER", "signing RPMs with %s through bsgo-signd at %s", helper, cfg.SigndAddr)
}

func fatalf(component, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[%s] [%s] %s\n", time.Now().Format("15:04:05"), component, fmt.Sprintf(format, args...))
	os.Exit(1)
}

// subscribeJobFinishedForSigner handles job-finished messages concurrently up
// to workers jobs. Signing downloads whole RPMs, so the bound protects memory
// and disk of the replica.
func subscribeJobFinishedForSigner(ctx context.Context, nc *nats.Conn, stage *signing.Stage, workers int) {
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	_, err := nc.QueueSubscribe(natsqueue.SubjectJobFinished, natsqueue.QueueSigner, func(msg *nats.Msg) {
		data := msg.Data
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			if err := stage.Handle(ctx, data); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] [SIGNER] %v\n", time.Now().Format("15:04:05"), err)
			}
		}()
	})
	if err != nil {
		fatalf("SIGNER", "subscribing to %s: %v", natsqueue.SubjectJobFinished, err)
	}
}

// subscribeJobSigned feeds the signer's answers to the state handler, which
// integrates the signed binaries.
func subscribeJobSigned(ctx context.Context, nc *nats.Conn, handle func(context.Context, []byte) error) {
	sem := make(chan struct{}, 8)
	_, err := nc.Subscribe(natsqueue.SubjectJobSigned, func(msg *nats.Msg) {
		data := msg.Data
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			if err := handle(ctx, data); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] [STATE] %v\n", time.Now().Format("15:04:05"), err)
			}
		}()
	})
	if err != nil {
		fatalf("STATE", "subscribing to %s: %v", natsqueue.SubjectJobSigned, err)
	}
}
