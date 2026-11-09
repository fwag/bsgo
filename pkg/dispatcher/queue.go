package dispatcher

import (
	"sort"
	"sync"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type PriorityQueue struct {
	mu   sync.Mutex
	jobs []*v1.BuildJob
}

func NewPriorityQueue() *PriorityQueue {
	return &PriorityQueue{
		jobs: make([]*v1.BuildJob, 0),
	}
}

func (q *PriorityQueue) Push(job *v1.BuildJob) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.jobs = append(q.jobs, job)
	q.sortJobs()
}

func (q *PriorityQueue) Peek() *v1.BuildJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.jobs) == 0 {
		return nil
	}
	return q.jobs[0]
}

func (q *PriorityQueue) Pop() *v1.BuildJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.jobs) == 0 {
		return nil
	}

	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job
}

func (q *PriorityQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

func (q *PriorityQueue) Remove(index int) *v1.BuildJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	if index < 0 || index >= len(q.jobs) {
		return nil
	}

	job := q.jobs[index]
	q.jobs = append(q.jobs[:index], q.jobs[index+1:]...)
	return job
}

func (q *PriorityQueue) RemoveJob(jobID string) *v1.BuildJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, j := range q.jobs {
		if j.JobID == jobID {
			job := q.jobs[i]
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return job
		}
	}
	return nil
}

// RemoveByBuildKey removes any pending job matching the given project, package, target, and arch.
func (q *PriorityQueue) RemoveByBuildKey(project, pkg, target, arch string) *v1.BuildJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, j := range q.jobs {
		if j.Project == project && j.Package == pkg && j.Target == target && j.Architecture == arch {
			job := q.jobs[i]
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return job
		}
	}
	return nil
}

func (q *PriorityQueue) Snapshot() []*v1.BuildJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]*v1.BuildJob, len(q.jobs))
	copy(out, q.jobs)
	return out
}

func (q *PriorityQueue) sortJobs() {
	sort.SliceStable(q.jobs, func(i, j int) bool {
		return q.jobs[i].Priority > q.jobs[j].Priority
	})
}
