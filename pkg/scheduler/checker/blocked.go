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
		reason.Cycles = append(reason.Cycles, storage.BlockedCycle{Held: members, InFlight: ps.inFlightMembers(g)})
	}
	sort.Slice(reason.Cycles, func(i, j int) bool {
		return reason.Cycles[i].Held[0].Unit < reason.Cycles[j].Held[0].Unit
	})
	return reason
}

// inFlightMembers lists the members of a cycle that are not ready by
// themselves: the ones being built or waiting for something.
func (ps *pass) inFlightMembers(g *group) []storage.BlockedProducer {
	var out []storage.BlockedProducer
	for _, m := range g.members {
		if ps.out.notready[m] && !ps.out.held[m] {
			out = append(out, storage.BlockedProducer{Unit: m.name, State: ps.out.state[m]})
		}
	}
	sortProducers(out)
	return out
}

func sortProducers(list []storage.BlockedProducer) {
	sort.Slice(list, func(i, j int) bool { return list[i].Unit < list[j].Unit })
}
