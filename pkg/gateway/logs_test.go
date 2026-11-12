package gateway_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/openSUSE/bsgo/pkg/gateway"
)

func runEmbeddedNATSForLogs(t *testing.T) (*server.Server, string) {
	t.Helper()
	opts := &server.Options{
		Port: -1,
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("failed starting embedded nats: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatalf("embedded nats server timed out starting")
	}
	t.Cleanup(func() { s.Shutdown() })
	return s, s.ClientURL()
}

type mockLogStorage struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *mockLogStorage) GetRepoFile(_ context.Context, path string) (io.ReadCloser, int64, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, ok := m.files[path]; ok {
		return io.NopCloser(strings.NewReader(string(data))), int64(len(data)), "text/plain", nil
	}
	return nil, 0, "", fmt.Errorf("file not found: %s", path)
}

func TestLogServerLiveSSEStreaming(t *testing.T) {
	_, natsURL := runEmbeddedNATSForLogs(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("failed connecting to nats: %v", err)
	}
	defer nc.Close()

	logServer := gateway.NewLogServer(nc, nil)
	ts := httptest.NewServer(logServer)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/jobs/job-stream-01/logs", nil)
	if err != nil {
		t.Fatalf("failed creating request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := ts.Client()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed executing SSE request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	// Give SSE subscription a moment to establish
	time.Sleep(50 * time.Millisecond)

	// Publish lines to NATS
	_ = nc.Publish("bsgo.logs.job-stream-01", []byte("line 1: compiling package"))
	_ = nc.Publish("bsgo.logs.job-stream-01", []byte("line 2: compilation finished"))
	_ = nc.Flush()

	scanner := bufio.NewScanner(resp.Body)
	received := make([]string, 0)

	for scanner.Scan() {
		text := scanner.Text()
		if strings.HasPrefix(text, "data: ") {
			received = append(received, strings.TrimPrefix(text, "data: "))
			if len(received) == 2 {
				break
			}
		}
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 lines received, got %d (%v)", len(received), received)
	}
	if received[0] != "line 1: compiling package" {
		t.Errorf("unexpected first line: %s", received[0])
	}
	if received[1] != "line 2: compilation finished" {
		t.Errorf("unexpected second line: %s", received[1])
	}
}

func TestLogServerArchivedLogReplay(t *testing.T) {
	storage := &mockLogStorage{
		files: map[string][]byte{
			"jobs/job-archived-01/build.log": []byte("archived line A\narchived line B\n"),
		},
	}

	logServer := gateway.NewLogServer(nil, storage)
	ts := httptest.NewServer(logServer)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/jobs/job-archived-01/logs", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("failed executing request: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	received := make([]string, 0)
	for scanner.Scan() {
		text := scanner.Text()
		if strings.HasPrefix(text, "data: ") {
			received = append(received, strings.TrimPrefix(text, "data: "))
			if len(received) == 2 {
				break
			}
		}
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 lines from archive, got %d", len(received))
	}
	if received[0] != "archived line A" || received[1] != "archived line B" {
		t.Errorf("unexpected archived lines: %v", received)
	}
}
