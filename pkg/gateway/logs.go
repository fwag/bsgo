package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// LogStorage abstracts retrieval of archived build logs.
type LogStorage interface {
	GetRepoFile(ctx context.Context, path string) (io.ReadCloser, int64, string, error)
}

// LogServer handles real-time log streaming via SSE and archived log replay.
type LogServer struct {
	nc      *nats.Conn
	storage LogStorage
}

// NewLogServer creates a new live log streaming server.
func NewLogServer(nc *nats.Conn, storage LogStorage) *LogServer {
	return &LogServer{
		nc:      nc,
		storage: storage,
	}
}

func (s *LogServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	jobID := parseJobIDFromPath(r.URL.Path)
	if jobID == "" {
		http.Error(w, "invalid job ID in path", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	s.replayArchivedLogs(r.Context(), jobID, w, flusher)
	s.streamLiveLogs(r.Context(), jobID, w, flusher)
}

func parseJobIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// Expected format: api/v1/jobs/{job_id}/logs
	for i, part := range parts {
		if part == "jobs" && i+2 < len(parts) && parts[i+2] == "logs" {
			candidate := parts[i+1]
			if candidate != "" && !strings.Contains(candidate, "..") {
				return filepath.Clean(candidate)
			}
		}
	}
	return ""
}

func (s *LogServer) replayArchivedLogs(ctx context.Context, jobID string, w io.Writer, flusher http.Flusher) {
	if s.storage == nil {
		return
	}

	candidates := []string{
		fmt.Sprintf("jobs/%s/build.log", jobID),
		fmt.Sprintf("logs/%s.log", jobID),
	}

	for _, p := range candidates {
		rc, _, _, err := s.storage.GetRepoFile(ctx, p)
		if err != nil {
			continue
		}
		defer rc.Close()

		scanner := bufio.NewScanner(rc)
		for scanner.Scan() {
			fmt.Fprintf(w, "data: %s\n\n", scanner.Text())
			flusher.Flush()
		}
		return
	}
}

func (s *LogServer) streamLiveLogs(ctx context.Context, jobID string, w io.Writer, flusher http.Flusher) {
	if s.nc == nil {
		return
	}

	logChan := make(chan string, 100)
	sub, err := s.nc.Subscribe("bsgo.logs."+jobID, func(msg *nats.Msg) {
		select {
		case logChan <- string(msg.Data):
		default:
		}
	})
	if err != nil {
		return
	}
	defer func() { _ = sub.Unsubscribe() }()
	_ = s.nc.Flush()

	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case line, ok := <-logChan:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	}
}
