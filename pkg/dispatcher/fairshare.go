package dispatcher

import (
	"math"
	"sort"
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type projectLoad struct {
	accumulatedSeconds float64
	lastUpdated        time.Time
}

// FairShareManager tracks moving wall-clock build time per project with exponential decay
// and calculates dynamic effective priority with staleness aging and source change bias.
type FairShareManager struct {
	mu           sync.Mutex
	lambda       float64 // decay rate per second: ln(2) / halfLife
	agingRate    float64 // priority boost per second waiting
	sourceBias   float64 // bonus points for direct source changes
	penaltyScale float64 // penalty per accumulated load second
	loads        map[string]*projectLoad
}

func NewFairShareManager(halfLife time.Duration, agingRate, sourceBias, penaltyScale float64) *FairShareManager {
	if halfLife <= 0 {
		halfLife = 30 * time.Minute
	}
	if agingRate <= 0 {
		agingRate = 0.05 // +3 priority points per minute of waiting
	}
	if sourceBias <= 0 {
		sourceBias = 100.0 // +100 bonus for interactive human commits
	}
	if penaltyScale <= 0 {
		penaltyScale = 0.01 // 100s of CPU time reduces score by 1 point
	}

	lambda := math.Ln2 / halfLife.Seconds()

	return &FairShareManager{
		lambda:       lambda,
		agingRate:    agingRate,
		sourceBias:   sourceBias,
		penaltyScale: penaltyScale,
		loads:        make(map[string]*projectLoad),
	}
}

// RecordBuildTime adds executed build duration to a project's load score with exponential decay.
func (f *FairShareManager) RecordBuildTime(project string, duration time.Duration, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	load, exists := f.loads[project]
	if !exists {
		f.loads[project] = &projectLoad{
			accumulatedSeconds: duration.Seconds(),
			lastUpdated:        now,
		}
		return
	}

	dt := now.Sub(load.lastUpdated).Seconds()
	if dt < 0 {
		dt = 0
	}

	decayed := load.accumulatedSeconds * math.Exp(-f.lambda*dt)
	load.accumulatedSeconds = decayed + duration.Seconds()
	load.lastUpdated = now
}

// GetProjectLoad returns the decayed load score for a project at time 'now'.
func (f *FairShareManager) GetProjectLoad(project string, now time.Time) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	load, exists := f.loads[project]
	if !exists {
		return 0
	}

	dt := now.Sub(load.lastUpdated).Seconds()
	if dt < 0 {
		dt = 0
	}
	return load.accumulatedSeconds * math.Exp(-f.lambda*dt)
}

// CalculateEffectivePriority computes the dynamic priority taking into account base priority,
// source change bias, staleness aging, and project load penalty.
func (f *FairShareManager) CalculateEffectivePriority(job *v1.BuildJob, now time.Time) float64 {
	baseScore := float64(job.Priority)

	// 1. Source change bias: reward direct user/git commits over automated cascades
	bias := 0.0
	if job.Reason == "SOURCE_CHANGE" || job.Reason == "ADMIN_TRIGGER" {
		bias = f.sourceBias
	}

	// 2. Staleness aging: older pending jobs gain priority over time
	createdAt := time.Unix(job.CreatedAtUnix, 0)
	waitDuration := now.Sub(createdAt).Seconds()
	if waitDuration < 0 {
		waitDuration = 0
	}
	stalenessBonus := waitDuration * f.agingRate

	// 3. Project load penalty: projects consuming heavy worker time are penalized
	load := f.GetProjectLoad(job.Project, now)
	penalty := load * f.penaltyScale

	return baseScore + bias + stalenessBonus - penalty
}

// SortJobsFairShare sorts a list of jobs in-place by effective dynamic priority descending.
func (f *FairShareManager) SortJobsFairShare(jobs []*v1.BuildJob, now time.Time) {
	if len(jobs) <= 1 {
		return
	}

	scores := make(map[*v1.BuildJob]float64, len(jobs))
	for _, j := range jobs {
		scores[j] = f.CalculateEffectivePriority(j, now)
	}

	sort.SliceStable(jobs, func(i, j int) bool {
		return scores[jobs[i]] > scores[jobs[j]]
	})
}
