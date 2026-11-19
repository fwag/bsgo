package checker

import (
	"sort"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// blockReason explains a blocked unit by the source packages that produce the
// binaries it waits for. A producer that is not ready by itself is reported
// with its state; producers that are not ready only because their dependency
// cycle is, are reported once per cycle with the members that are in flight.
func (ps *pass) blockReason(binaries []string) *storage.BlockedReason {
	byUnit := map[*unit][]string{}
	for _, n := range binaries {
		if p := ps.prod[n]; p != nil {
			byUnit[p] = append(byUnit[p], n)
		}
	}
	reason := &storage.BlockedReason{}
	held := map[*group][]storage.BlockedProducer{}
	for p, bins := range byUnit {
		sort.Strings(bins)
		bp := storage.BlockedProducer{Unit: p.name, State: ps.out.state[p], Binaries: unique(bins)}
		if ps.out.held[p] && p.group != nil {
			held[p.group] = append(held[p.group], bp)
			continue
		}
		reason.Producers = append(reason.Producers, bp)
	}
	sortProducers(reason.Producers)
	for g, members := range held {
		sortProducers(members)
		reason.Cycles = append(reason.Cycles, ps.cycleReason(g, members))
	}
	sort.Slice(reason.Cycles, func(i, j int) bool {
		return reason.Cycles[i].Held[0].Unit < reason.Cycles[j].Held[0].Unit
	})
	return reason
}

// cycleReason describes an unfinished cycle: the members being worked on,
// building first, and how many are blocked themselves.
func (ps *pass) cycleReason(g *group, held []storage.BlockedProducer) storage.BlockedCycle {
	c := storage.BlockedCycle{Held: held, InFlight: []storage.BlockedProducer{}, Members: len(g.members)}
	for _, m := range g.members {
		state := ps.out.state[m]
		switch {
		case state == storage.StateBlocked:
			c.BlockedMembers++
		case ps.out.notready[m] && !ps.out.held[m]:
			c.InFlight = append(c.InFlight, storage.BlockedProducer{Unit: m.name, State: state})
		}
	}
	sort.Slice(c.InFlight, func(i, j int) bool {
		a, b := c.InFlight[i], c.InFlight[j]
		if ra, rb := flightRank(a.State), flightRank(b.State); ra != rb {
			return ra < rb
		}
		return a.Unit < b.Unit
	})
	return c
}

// flightRank orders the states of members in flight: building is the most
// telling one.
func flightRank(state string) int {
	switch state {
	case storage.StateBuilding:
		return 0
	case storage.StateSigning, storage.StateFinished:
		return 1
	case storage.StateDispatching:
		return 2
	}
	return 3
}

func sortProducers(list []storage.BlockedProducer) {
	sort.Slice(list, func(i, j int) bool { return list[i].Unit < list[j].Unit })
}
