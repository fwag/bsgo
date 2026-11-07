package scheduler

import (
	"sync"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

// PrioritizedTriggerQueue implements a 3-tier trigger queue with anti-starvation scheduling.
// Modeled after OBS nextlookat() anti-starvation mechanics:
// If notLowCount >= maxConsecutiveNonLow (default 10), it forces a check into the low queue.
type PrioritizedTriggerQueue struct {
	mu                   sync.Mutex
	highQueue            []*v1.BuildTrigger
	medQueue             []*v1.BuildTrigger
	lowQueue             []*v1.BuildTrigger
	notLowCount          int
	maxConsecutiveNonLow int
}

func NewPrioritizedTriggerQueue(maxConsecutiveNonLow int) *PrioritizedTriggerQueue {
	if maxConsecutiveNonLow <= 0 {
		maxConsecutiveNonLow = 10
	}
	return &PrioritizedTriggerQueue{
		highQueue:            make([]*v1.BuildTrigger, 0),
		medQueue:             make([]*v1.BuildTrigger, 0),
		lowQueue:             make([]*v1.BuildTrigger, 0),
		maxConsecutiveNonLow: maxConsecutiveNonLow,
	}
}

func (q *PrioritizedTriggerQueue) Push(trigger *v1.BuildTrigger) {
	q.mu.Lock()
	defer q.mu.Unlock()

	switch trigger.NormalizedPriority() {
	case "low":
		q.lowQueue = append(q.lowQueue, trigger)
	case "med":
		q.medQueue = append(q.medQueue, trigger)
	default:
		q.highQueue = append(q.highQueue, trigger)
	}
}

// Pop extracts the next trigger according to priority tier and anti-starvation rules.
func (q *PrioritizedTriggerQueue) Pop() *v1.BuildTrigger {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Anti-starvation check: force check into lowQueue if notLowCount threshold exceeded
	if q.notLowCount >= q.maxConsecutiveNonLow && len(q.lowQueue) > 0 {
		q.notLowCount = 0
		return q.popLow()
	}

	if len(q.highQueue) > 0 {
		q.notLowCount++
		return q.popHigh()
	}

	if len(q.medQueue) > 0 {
		q.notLowCount++
		return q.popMed()
	}

	if len(q.lowQueue) > 0 {
		q.notLowCount = 0
		return q.popLow()
	}

	return nil
}

func (q *PrioritizedTriggerQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.highQueue) + len(q.medQueue) + len(q.lowQueue)
}

func (q *PrioritizedTriggerQueue) Counts() (high, med, low int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.highQueue), len(q.medQueue), len(q.lowQueue)
}

func (q *PrioritizedTriggerQueue) popHigh() *v1.BuildTrigger {
	tr := q.highQueue[0]
	q.highQueue = q.highQueue[1:]
	return tr
}

func (q *PrioritizedTriggerQueue) popMed() *v1.BuildTrigger {
	tr := q.medQueue[0]
	q.medQueue = q.medQueue[1:]
	return tr
}

func (q *PrioritizedTriggerQueue) popLow() *v1.BuildTrigger {
	tr := q.lowQueue[0]
	q.lowQueue = q.lowQueue[1:]
	return tr
}
