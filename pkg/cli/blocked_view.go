package cli

import (
	"fmt"
	"strings"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// blockedLines renders the full reason of a blocked package, one source
// package or cycle per line, each line starting with indent.
func blockedLines(r *storage.BlockedReason, indent string) []string {
	var lines []string
	for _, p := range r.Producers {
		lines = append(lines, indent+producerText(p))
	}
	for _, c := range r.Cycles {
		lines = append(lines, indent+"dependency cycle not finished; in flight: "+membersText(c.InFlight))
		for _, h := range c.Held {
			lines = append(lines, indent+"  held only by the cycle: "+producerText(h))
		}
	}
	return lines
}

// producerText is "unit (state)" followed by the binaries the package needs.
func producerText(p storage.BlockedProducer) string {
	s := fmt.Sprintf("%s (%s)", p.Unit, p.State)
	if len(p.Binaries) > 0 {
		s += " provides " + strings.Join(p.Binaries, ", ")
	}
	return s
}

func membersText(list []storage.BlockedProducer) string {
	if len(list) == 0 {
		return "none"
	}
	parts := make([]string, len(list))
	for i, m := range list {
		parts[i] = m.Unit + " " + m.State
	}
	return strings.Join(parts, ", ")
}
