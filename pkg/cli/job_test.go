package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestCLIJobLogsStreaming(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/jobs/job-cli-01/logs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: [12:00:00] starting build\n\ndata: [12:00:01] compilation complete\n\ndata: build SUCCEEDED\n\n")
	}))
	defer ts.Close()

	store := storage.NewMemoryStore()
	cmd := cli.NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)

	cmd.SetArgs([]string{"job", "logs", "job-cli-01", "--server", ts.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("failed executing job logs: %v", err)
	}

	output := outBuf.String()
	if !strings.Contains(output, "starting build") {
		t.Errorf("expected 'starting build' in output, got: %s", output)
	}
	if !strings.Contains(output, "compilation complete") {
		t.Errorf("expected 'compilation complete' in output, got: %s", output)
	}
}

func TestCLIJobLogsJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: step 1\n\ndata: build SUCCEEDED\n\n")
	}))
	defer ts.Close()

	store := storage.NewMemoryStore()
	cmd := cli.NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)

	cmd.SetArgs([]string{"job", "logs", "job-json-01", "--server", ts.URL, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("failed executing job logs: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected JSON output lines, got empty")
	}

	var parsed map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("failed unmarshalling JSON line %q: %v", lines[0], err)
	}

	if parsed["job_id"] != "job-json-01" {
		t.Errorf("expected job_id job-json-01, got %s", parsed["job_id"])
	}
	if parsed["log"] != "step 1" {
		t.Errorf("expected log 'step 1', got %s", parsed["log"])
	}
}
