package ear

import (
	"context"
	"runtime"
	"sync"
)

// parallelMap applies fn to every item and returns the results in input
// order. Each goroutine writes only its own result slot, so there is no
// shared-write race and no lock on the hot path. For a single item (or a
// nil/tautologically-serial workload) it runs inline, so the trivial case
// pays nothing for the machinery.
//
// This is the seam EAR's governance and discovery stages want: judgment is
// independent per policy / per process, and in the live runtime each
// judgment is a network round-trip to a model. Fanning them out turns a
// sum of latencies into a max of latencies. The deterministic fallbacks are
// cheap enough that order-preserving parallelism is invisible; the shape is
// here for when the judge is an LLM.
func parallelMap[T, R any](ctx context.Context, items []T, fn func(context.Context, T) R) []R {
	results := make([]R, len(items))
	if len(items) <= 1 {
		for i, item := range items {
			results[i] = fn(ctx, item)
		}
		return results
	}

	// Bound concurrency to GOMAXPROCS so a stack with hundreds of policies
	// doesn't spawn hundreds of simultaneous provider calls.
	limit := runtime.GOMAXPROCS(0)
	if limit > len(items) {
		limit = len(items)
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	wg.Add(len(items))
	for i := range items {
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fn(ctx, items[i])
		}(i)
	}
	wg.Wait()
	return results
}
