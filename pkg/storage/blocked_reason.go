package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxBlockedSummary bounds the items named in the details of a blocked row.
const MaxBlockedSummary = 3

// BlockedProducer is one source package (unit, "pkg" or "pkg:flavor") that
// keeps another one from building, with its own state.
type BlockedProducer struct {
	Unit  string `json:"unit"`
	State string `json:"state"`
	// Binaries are the binary packages of the unit the blocked package needs.
	Binaries []string `json:"binaries,omitempty"`
}

// BlockedCycle is a dependency cycle that is not finished. Its Held members
// are not ready only because of it (OBS marks a whole cycle not ready while
// one member is). InFlight are the members being worked on (building,
// signing, dispatching, scheduled), building first, then by name. Members that
// are blocked themselves are not the cause and only counted in
// BlockedMembers; Members is the size of the cycle.
type BlockedCycle struct {
	Held     []BlockedProducer `json:"held"`
	InFlight []BlockedProducer `json:"in_flight"`
	// Members is the number of units of the cycle, BlockedMembers how many of
	// them are blocked.
	Members        int `json:"members"`
	BlockedMembers int `json:"blocked_members,omitempty"`
}

// BlockedReason is the structured cause of a blocked build state: the
// producers that are not ready by themselves and the unfinished cycles. Both
// lists are deduplicated and sorted.
type BlockedReason struct {
	Producers []BlockedProducer `json:"producers,omitempty"`
	Cycles    []BlockedCycle    `json:"cycles,omitempty"`
}

// Equal reports whether two reasons are identical.
func (r *BlockedReason) Equal(o *BlockedReason) bool {
	a, errA := json.Marshal(r)
	b, errB := json.Marshal(o)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// Summary is the details text of a blocked row: "blocked: " and at most
// MaxBlockedSummary items, then "+N more". Producers come first.
func (r *BlockedReason) Summary() string {
	var items []string
	for _, p := range r.Producers {
		items = append(items, fmt.Sprintf("%s (%s)", p.Unit, p.State))
	}
	for _, c := range r.Cycles {
		items = append(items, "dependency cycle not finished ("+inFlightText(c)+")")
	}
	more := ""
	if len(items) > MaxBlockedSummary {
		more = fmt.Sprintf(" +%d more", len(items)-MaxBlockedSummary)
		items = items[:MaxBlockedSummary]
	}
	return "blocked: " + strings.Join(items, ", ") + more
}

func inFlightText(c BlockedCycle) string {
	var parts []string
	for _, p := range c.InFlight {
		parts = append(parts, p.Unit+" "+p.State)
	}
	if len(parts) > MaxBlockedSummary {
		n := len(parts) - MaxBlockedSummary
		parts = append(parts[:MaxBlockedSummary], fmt.Sprintf("+%d more", n))
	}
	text := strings.Join(parts, ", ")
	if text == "" {
		text = "none in flight"
	}
	if c.BlockedMembers > 0 {
		text += fmt.Sprintf("; %d blocked", c.BlockedMembers)
	}
	return text
}
