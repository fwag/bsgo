package worker

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunWatchedOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		lim         buildLimits
		cancelAfter time.Duration
		wantErr     string
		wantAbort   bool
		wantExit    int
	}{
		{name: "success", script: "echo ok"},
		{name: "build failure is FAILED not ABORTED", script: "echo boom; exit 3", wantErr: "exit status 3", wantExit: 3},
		{name: "killed by signal is ABORTED", script: "kill -9 $$", wantErr: "killed by signal", wantAbort: true, wantExit: 137},
		{name: "idle limit", script: "echo start; sleep 30", lim: buildLimits{idle: 300 * time.Millisecond}, wantErr: "logidlelimit", wantAbort: true, wantExit: 137},
		{name: "chatty build is not idle", script: "for i in 1 2 3 4 5 6; do echo $i; sleep 0.1; done", lim: buildLimits{idle: 400 * time.Millisecond}},
		{name: "timeout", script: "sleep 30", lim: buildLimits{timeout: 300 * time.Millisecond}, wantErr: "timeout", wantAbort: true, wantExit: 137},
		{name: "abort request", script: "sleep 30", cancelAfter: 200 * time.Millisecond, wantErr: "cancelled", wantAbort: true, wantExit: 137},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelAfter > 0 {
				time.AfterFunc(tc.cancelAfter, cancel)
			}
			var out bytes.Buffer
			got := runWatched(ctx, "/bin/sh", []string{"-c", tc.script}, t.TempDir(), tc.lim, &out)
			if tc.wantErr == "" {
				if got.err != nil {
					t.Fatalf("unexpected error %v", got.err)
				}
				return
			}
			if got.err == nil || !strings.Contains(got.err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", got.err, tc.wantErr)
			}
			if got.aborted != tc.wantAbort || got.exitCode != tc.wantExit {
				t.Errorf("aborted=%v exit=%d, want aborted=%v exit=%d", got.aborted, got.exitCode, tc.wantAbort, tc.wantExit)
			}
		})
	}
}

func TestDetermineJobOutcomeAborted(t *testing.T) {
	phase, code := determineJobOutcome(context.Background(), context.DeadlineExceeded, &ExecutionResult{ExitCode: 137, Aborted: true})
	if phase != "ABORTED" || code != 137 {
		t.Errorf("got %s/%d", phase, code)
	}
	phase, _ = determineJobOutcome(context.Background(), context.DeadlineExceeded, &ExecutionResult{ExitCode: 2})
	if phase != "FAILED" {
		t.Errorf("genuine failure reported as %s", phase)
	}
}

func TestJobBuildRootUniqueMachineName(t *testing.T) {
	tests := []struct {
		jobDir, want string
	}{
		{"/w/jobs/a5bedcc7-eb9e-5c53-8317-0ba36072036f", "/w/jobs/a5bedcc7-eb9e-5c53-8317-0ba36072036f/root-a5bedcc7-eb9e-5c53-8317-0ba36072036f"},
		{"/w/jobs/x", "/w/jobs/x/root-x"},
	}
	for _, tt := range tests {
		got := jobBuildRoot(tt.jobDir)
		if got != tt.want {
			t.Errorf("jobBuildRoot(%q) = %q, want %q", tt.jobDir, got, tt.want)
		}
		if name := "obsbuild." + filepath.Base(got); len(name) > 64 {
			t.Errorf("machine name %q exceeds 64 characters", name)
		}
	}
}
