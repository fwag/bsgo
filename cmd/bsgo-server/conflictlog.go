package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/openSUSE/bsgo/pkg/publisher"
)

// maxConflictLines bounds the log lines of one changed conflict set.
const maxConflictLines = 5

// conflictReporter turns the file name conflicts of publishes into log lines,
// but only when the conflict set of a prpa changed since its last publish, so
// that the frequent publishes of a prpa with a standing conflict stay quiet.
type conflictReporter struct {
	mu   sync.Mutex
	last map[string]string
}

// lines returns the lines to log for the conflicts of prpa: none when they
// are the same as last time, at most maxConflictLines plus a summary otherwise.
func (r *conflictReporter) lines(prpa string, conflicts []publisher.NameConflict) []string {
	sig := conflictSignature(conflicts)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil {
		r.last = map[string]string{}
	}
	if r.last[prpa] == sig {
		return nil
	}
	if sig == "" {
		delete(r.last, prpa)
		return nil
	}
	r.last[prpa] = sig
	var out []string
	for i, c := range conflicts {
		if i == maxConflictLines {
			out = append(out, fmt.Sprintf("%s: and %d more file names produced by different units", prpa, len(conflicts)-i))
			break
		}
		out = append(out, fmt.Sprintf("%s: file name %s is produced by different units, published %s, ignored %s",
			prpa, c.Filename, c.Kept, strings.Join(c.Dropped, ", ")))
	}
	return out
}

func conflictSignature(conflicts []publisher.NameConflict) string {
	var b strings.Builder
	for _, c := range conflicts {
		fmt.Fprintf(&b, "%s|%s|%s\n", c.Filename, c.Kept, strings.Join(c.Dropped, ","))
	}
	return b.String()
}
