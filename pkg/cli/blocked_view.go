package cli

import (
	"fmt"
	"strings"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// maxMembersShown bounds the members named per state in a cycle heading
// unless the caller asks for everything.
const maxMembersShown = 5

// blockedLines renders the full reason of a blocked package, one source
// package per line and one heading per cycle, each line starting with indent.
func blockedLines(r *storage.BlockedReason, indent string) []string {
	var lines []string
	for _, p := range r.Producers {
		lines = append(lines, indent+producerText(p))
	}
	for _, c := range r.Cycles {
		lines = append(lines, indent+cycleHeading(c, true))
		for _, h := range c.Held {
			lines = append(lines, indent+"  "+producerText(h))
		}
	}
	return lines
}

// producerText is "unit (state)" followed by the binaries the package needs.
func producerText(p storage.BlockedProducer) string {
	s := fmt.Sprintf("%s (%s)", p.Unit, p.State)
	if len(p.Binaries) > 0 {
		s += ": " + strings.Join(p.Binaries, ", ")
	}
	return s
}

// cycleHeading names an unfinished cycle once: its size and the members in
// flight grouped by state (building first), the blocked ones as a count. Unless
// all is set at most maxMembersShown members are named per state.
func cycleHeading(c storage.BlockedCycle, all bool) string {
	var groups []string
	for i := 0; i < len(c.InFlight); {
		state, names := c.InFlight[i].State, []string{}
		for ; i < len(c.InFlight) && c.InFlight[i].State == state; i++ {
			names = append(names, c.InFlight[i].Unit)
		}
		more := ""
		if !all && len(names) > maxMembersShown {
			more = fmt.Sprintf(", ... (+%d more)", len(names)-maxMembersShown)
			names = names[:maxMembersShown]
		}
		groups = append(groups, state+": "+strings.Join(names, ", ")+more)
	}
	if len(groups) == 0 {
		groups = append(groups, "none in flight")
	}
	text := strings.Join(groups, "; ")
	if c.BlockedMembers > 0 {
		text += fmt.Sprintf("; %d blocked", c.BlockedMembers)
	}
	return fmt.Sprintf("held by unfinished dependency cycle (%d members): %s", c.Members, text)
}
