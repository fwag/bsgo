// Package gc removes objects from the artifact bucket that no current pointer
// can reach: superseded build repository indexes below buildrepo/ and the
// binaries of superseded builds below artifacts/.
//
// The rules are the same for both prefixes. Only objects older than the grace
// period are ever deleted, so a reader that loaded a pointer a moment ago (a
// checker pass, a publisher run, a job downloading its build dependencies)
// never misses an object. Everything a current pointer references is kept.
// Pointers are re-read right before objects are deleted, so a pointer swap
// that happens during a run can never lose the new current objects.
package gc

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Prefixes of the bucket that GC manages. repos/, backup/, recipes/ and dod/
// are never touched.
const (
	PrefixBuildRepo = "buildrepo"
	PrefixArtifacts = "artifacts"
)

// Defaults of Options.
const (
	DefaultGrace        = 24 * time.Hour
	DefaultKeepVersions = 3
)

// MaxBatch is the number of keys deleted per call, the S3 DeleteObjects limit.
const MaxBatch = 1000

// Scope is the access token scope that allows running GC through an API.
const Scope = "admin:gc"

// Object is the listing entry of one stored object.
type Object struct {
	Key          string
	LastModified time.Time
	Size         int64
}

// Objects is the object storage GC works on.
type Objects interface {
	// List returns all objects whose key starts with prefix.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Get returns the content of one object.
	Get(ctx context.Context, key string) ([]byte, error)
	// DeleteBatch deletes up to MaxBatch objects; missing keys are not an error.
	DeleteBatch(ctx context.Context, keys []string) error
}

// Options tune what a run keeps.
type Options struct {
	// Grace is the minimum age of an object before it can be deleted.
	Grace time.Duration
	// KeepVersions is the number of superseded index versions kept per prpa
	// in addition to the current one.
	KeepVersions int
	// Project restricts the run to one project; empty means all.
	Project string
}

// Plan is what GC does to one prpa (project/repo/arch) below one prefix.
type Plan struct {
	Prefix string `json:"prefix"`
	Prpa   string `json:"prpa"`
	// Keep lists the kept objects; Kept counts them.
	Keep      []string `json:"-"`
	Kept      int      `json:"kept"`
	KeptBytes int64    `json:"kept_bytes"`
	Delete    []string `json:"delete"`
	// FreeBytes is the size of the objects in Delete.
	FreeBytes int64 `json:"free_bytes"`
	// Deleted is the number of objects deleted by Apply, or that would be in
	// a dry run.
	Deleted int `json:"deleted"`
	// Warnings are problems with single units; the affected unit was kept as
	// a whole and the rest of the plan stands.
	Warnings []string `json:"warnings,omitempty"`
	// Error is set when planning or applying this prpa failed.
	Error string `json:"error,omitempty"`
}

func (p *Plan) keep(o Object) {
	p.Keep = append(p.Keep, o.Key)
	p.KeptBytes += o.Size
}

func (p *Plan) drop(o Object) {
	p.Delete = append(p.Delete, o.Key)
	p.FreeBytes += o.Size
}

func (p *Plan) finish() {
	sort.Strings(p.Keep)
	sort.Strings(p.Delete)
	p.Kept = len(p.Keep)
}

// Result is the outcome of one prefix.
type Result struct {
	Prefix string `json:"prefix"`
	DryRun bool   `json:"dry_run"`
	Plans  []Plan `json:"plans"`
	Kept   int    `json:"kept"`
	// Deleted counts deleted objects; in a dry run, objects that would be.
	Deleted    int   `json:"deleted"`
	FreedBytes int64 `json:"freed_bytes"`
	Errors     int   `json:"errors"`
	Warnings   int   `json:"warnings"`
	// Duration is the wall time of the prefix; not part of the JSON so that
	// equal runs produce equal output.
	Duration time.Duration `json:"-"`
}

// Protected returns the keys that the current pointers reference.
type Protected func(ctx context.Context) (map[string]struct{}, error)

// Apply executes plans. The protected set is read again right before each
// plan's deletions and every protected key is skipped, so pointers that moved
// since planning are honoured. A failing plan records its error and Apply
// continues with the next one. With dryRun nothing is deleted and Deleted
// reports what would be.
func Apply(ctx context.Context, objs Objects, plans []Plan, dryRun bool, protected Protected) []Plan {
	out := make([]Plan, len(plans))
	copy(out, plans)
	for i := range out {
		p := &out[i]
		if dryRun {
			p.Deleted = len(p.Delete)
			continue
		}
		if p.Error != "" {
			continue
		}
		n, err := applyPlan(ctx, objs, p, protected)
		p.Deleted = n
		if err != nil {
			p.Error = err.Error()
		}
	}
	return out
}

func applyPlan(ctx context.Context, objs Objects, p *Plan, protected Protected) (int, error) {
	if len(p.Delete) == 0 {
		return 0, nil
	}
	cur, err := protected(ctx)
	if err != nil {
		return 0, fmt.Errorf("re-reading pointers: %w", err)
	}
	keys := make([]string, 0, len(p.Delete))
	for _, k := range p.Delete {
		if _, ok := cur[k]; !ok {
			keys = append(keys, k)
		}
	}
	deleted := 0
	for start := 0; start < len(keys); start += MaxBatch {
		batch := keys[start:min(start+MaxBatch, len(keys))]
		if err := objs.DeleteBatch(ctx, batch); err != nil {
			return deleted, fmt.Errorf("deleting objects: %w", err)
		}
		deleted += len(batch)
	}
	return deleted, nil
}

// summarize fills the totals of a Result from its plans.
func summarize(prefix string, plans []Plan, dryRun bool, took time.Duration) Result {
	r := Result{Prefix: prefix, DryRun: dryRun, Plans: plans, Duration: took}
	for _, p := range plans {
		r.Kept += p.Kept
		r.Deleted += p.Deleted
		if dryRun || p.Deleted == len(p.Delete) {
			r.FreedBytes += p.FreeBytes
		}
		r.Warnings += len(p.Warnings)
		if p.Error != "" {
			r.Errors++
		}
	}
	return r
}
