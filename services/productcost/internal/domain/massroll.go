package domain

import (
	"sort"
	"sync"
	"time"
)

// RollRun is the outcome of a mass roll: one Result per item, sorted by id.
type RollRun struct {
	Results    []Result `json:"results"`
	ItemCount  int      `json:"itemCount"`
	ErrorCount int      `json:"errorCount"`
	DurationMs int64    `json:"durationMs"`
}

// MassRoll rolls every item in cat. Items are grouped by level and each level
// runs in parallel with opts.Workers goroutines, so a shared subassembly is
// costed once and reused by every parent (FR-39.5). Items that cannot roll
// (cycles, depth, missing components) get an Error result with diagnostics;
// the rest of the run continues (AC-39.8).
func MassRoll(cat Catalog, opts RollOptions) RollRun {
	opts = opts.normalized()
	start := time.Now()
	nodes := planRoll(cat, opts.MaxLevels)

	results := make(map[string]Result, len(cat))
	byLevel := map[int][]string{}
	maxLevel := 0
	for id, n := range nodes {
		if !n.ok {
			results[id] = Result{ItemID: id, Status: StatusError, Level: n.level, Own: Split{}, Rolled: Split{}, Diagnostics: n.diags}
			continue
		}
		byLevel[n.level] = append(byLevel[n.level], id)
		if n.level > maxLevel {
			maxLevel = n.level
		}
	}

	for lvl := 0; lvl <= maxLevel; lvl++ {
		ids := byLevel[lvl]
		sort.Strings(ids)
		out := make([]Result, len(ids))
		rollLevel(cat, results, ids, out, opts)
		for i, id := range ids {
			out[i].Level = lvl
			results[id] = out[i]
		}
	}

	run := RollRun{Results: make([]Result, 0, len(results)), ItemCount: len(results)}
	for _, id := range sortedKeys(results) {
		r := results[id]
		if r.Status == StatusError {
			run.ErrorCount++
		}
		run.Results = append(run.Results, r)
	}
	run.DurationMs = time.Since(start).Milliseconds()
	return run
}

// rollLevel rolls the items of one level concurrently. Each goroutine writes
// only its own slot of out; done is read-only while the level runs.
func rollLevel(cat Catalog, done map[string]Result, ids []string, out []Result, opts RollOptions) {
	sem := make(chan struct{}, opts.Workers)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()
			out[i] = rollItem(cat, cat[id], done, opts)
		}()
	}
	wg.Wait()
}

// RollOne returns the rolled result for id. Only the items reachable from id
// through its BOM are rolled, so a single calculation does not pay for the
// whole catalog. Unknown children stay in the subgraph and fail the parent.
func RollOne(cat Catalog, id string, opts RollOptions) (Result, bool) {
	if _, ok := cat[id]; !ok {
		return Result{}, false
	}
	sub := Catalog{}
	stack := []string{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, seen := sub[cur]; seen {
			continue
		}
		it, known := cat[cur]
		if !known {
			continue
		}
		sub[cur] = it
		for _, line := range it.BOM {
			stack = append(stack, line.ChildID)
		}
	}
	run := MassRoll(sub, opts)
	for _, r := range run.Results {
		if r.ItemID == id {
			return r, true
		}
	}
	return Result{}, false
}
