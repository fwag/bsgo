package history

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tempPrefix names the throwaway repositories of Resolve.
const tempPrefix = "bsgo-history-"

// SweepTemp removes throwaway repositories left in the temp directory by a
// process that was killed during a resolution. Only directories older than
// maxAge are removed, so one a live resolver of another process on the host is
// working in stays. It returns the number removed.
func SweepTemp(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < maxAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(os.TempDir(), e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
