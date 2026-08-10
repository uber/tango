package tgb

import (
	"runtime"
	"slices"
	"sync"
)

// parallelSortFunc sorts s with cmp using parallel chunk sorts joined by
// pairwise merges. The two encode-side sorts (2.8M nodeInfo, 2.8M names) are
// the largest serial stages of an encode; both are pure string-compare work
// that shards cleanly.
//
// depth bounds the fan-out at 2^depth goroutines. The final merge is a
// single serial pass, so the speedup ceiling is roughly 2x-4x on string
// keys — worth having, but this is not a linear scaler.
func parallelSortFunc[T any](s []T, cmp func(a, b T) int) {
	if len(s) < 1<<15 {
		slices.SortFunc(s, cmp)
		return
	}
	depth := 0
	for c := runtime.GOMAXPROCS(0); c > 1 && depth < 4; c >>= 1 {
		depth++
	}
	buf := make([]T, len(s))
	sortMerge(s, buf, cmp, depth)
}

func sortMerge[T any](s, buf []T, cmp func(a, b T) int, depth int) {
	if depth == 0 || len(s) < 4096 {
		slices.SortFunc(s, cmp)
		return
	}
	m := len(s) / 2
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sortMerge(s[:m], buf[:m], cmp, depth-1)
	}()
	sortMerge(s[m:], buf[m:], cmp, depth-1)
	wg.Wait()

	// Merge the two sorted halves into buf, then copy back.
	i, j, k := 0, m, 0
	for i < m && j < len(s) {
		if cmp(s[i], s[j]) <= 0 {
			buf[k] = s[i]
			i++
		} else {
			buf[k] = s[j]
			j++
		}
		k++
	}
	copy(buf[k:], s[i:m])
	copy(buf[k+m-i:], s[j:])
	copy(s, buf)
}
