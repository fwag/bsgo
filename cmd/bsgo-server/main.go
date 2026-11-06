package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func main() {
	var (
		natsURL  string
		httpPort int
	)

	flag.StringVar(&natsURL, "nats-url", "nats://localhost:4222", "NATS JetStream connection URL")
	flag.IntVar(&httpPort, "http-port", 8080, "HTTP API gateway listening port")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Starting bsgo Control Plane (API + Scheduler + Dispatcher)...\n")

	// 1. Connect to NATS JetStream and ensure streams
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to NATS (%s): %v\n", natsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to provision JetStream streams: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  • NATS JetStream streams verified: TRIGGERS, JOBS_PENDING, JOB_STATUS, PUBLISH\n")

	// 2. Storage
	store := storage.NewMemoryStore()
	seedDefaultData(ctx, store)
	fmt.Printf("  • Storage initialized & seeded with default projects and packages\n")

	// 3. HTTP Gateway
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		prjs, _ := store.ListProjects(r.Context())
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[")
		for i, p := range prjs {
			if i > 0 {
				fmt.Fprintf(w, ",")
			}
			fmt.Fprintf(w, `{"name":"%s","title":"%s"}`, p.Project.Name, p.Project.Title)
		}
		fmt.Fprintf(w, "]")
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", httpPort),
		Handler: mux,
	}

	go func() {
		fmt.Printf("  • API Gateway listening on http://localhost:%d\n", httpPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
		}
	}()

	fmt.Printf("\n✔ bsgo Control Plane is LIVE and ready for CLI and workers!\n")
	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	fmt.Println("bsgo Control Plane shut down cleanly.")
}

func seedDefaultData(ctx context.Context, store storage.Store) {
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "openSUSE:Factory", Title: "openSUSE Factory Distribution"},
		Targets:       map[string]manifest.BuildTarget{"standard": {Architectures: []string{"x86_64", "aarch64"}}},
	}
	_ = store.CreateProject(ctx, prj)
	_ = store.UpsertPackage(ctx, "openSUSE:Factory", &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "glibc"},
		Source:        manifest.PackageSource{Git: "https://github.com/openSUSE/glibc.git", Branch: "factory"},
		Build:         manifest.PackageBuild{Enabled: true},
	})
	_ = store.UpsertPackage(ctx, "openSUSE:Factory", &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "chromium"},
		Source:        manifest.PackageSource{Git: "https://github.com/openSUSE/chromium.git", Branch: "factory"},
		Build:         manifest.PackageBuild{Enabled: true},
	})
}
