package server

import (
	"fmt"
	"testing"
)

// makeGroups creates N groups each containing a single sqFlat whose ID is
// its index as a string. Handy for randomisation tests.
func makeGroups(n int) []group {
	groups := make([]group, n)
	for i := 0; i < n; i++ {
		groups[i] = group{{ID: fmt.Sprintf("g%02d", i)}}
	}
	return groups
}

// Pass 2 of greedyFill historically always picked the first same-size group
// in the remaining list — producing a deterministic tail of the exam.  The
// RNG-aware version should yield >1 distinct choices over many runs.
func TestGreedyFillPass2RandomisesTies(t *testing.T) {
	// 3 groups of size 2, target 4 — after first pass picks one (cost 2),
	// remaining has two size-2 groups tying for best fit in the gap of 2.
	picksTail := map[string]int{}
	for seed := 1; seed <= 200; seed++ {
		pool := []group{
			{{ID: "A"}, {ID: "A2"}},
			{{ID: "B"}, {ID: "B2"}},
			{{ID: "C"}, {ID: "C2"}},
		}
		// Shuffle deterministically per seed to vary first-pass pick
		rng := newRNG(fmt.Sprintf("seed-%d", seed))
		rng.shuffle(pool)
		got := greedyFillRNG(pool, 4, rng)
		if len(got) == 0 {
			t.Fatal("expected non-empty result")
		}
		last := got[len(got)-1][0].ID
		picksTail[last]++
	}
	if len(picksTail) < 3 {
		t.Fatalf("expected all 3 groups to appear as tail across 200 runs, got %v", picksTail)
	}
}

// Pass 3 (truncation) used to always pick the smallest oversize group and
// always take its first N sub-questions. With RNG, tie-breaking and window
// start should both vary.
func TestGreedyFillPass3RandomisesTruncation(t *testing.T) {
	// Target 3. Pool has three groups of size 5 — all tie for smallest
	// oversize (none fit in a gap of 3 exactly). greedyFillRNG should:
	//   1. pick one of the three groups at random
	//   2. start the 3-item window at a random position in 0..2
	pickedIDs := map[string]int{}
	windowStarts := map[string]int{}
	for seed := 1; seed <= 300; seed++ {
		pool := []group{
			makeFixedGroup("A", 5),
			makeFixedGroup("B", 5),
			makeFixedGroup("C", 5),
		}
		rng := newRNG(fmt.Sprintf("seed-%d", seed))
		got := greedyFillRNG(pool, 3, rng)
		if len(got) != 1 || len(got[0]) != 3 {
			t.Fatalf("expected single 3-item group, got %v", got)
		}
		firstID := got[0][0].ID
		prefix := firstID[:1]   // e.g. "A"
		start := firstID[2:]    // e.g. "0" from "A-0"
		pickedIDs[prefix]++
		windowStarts[start]++
	}
	if len(pickedIDs) < 3 {
		t.Fatalf("pass 3 should randomise tie-breaking over 300 runs, got %v", pickedIDs)
	}
	if len(windowStarts) < 2 {
		t.Fatalf("pass 3 window start should vary, got %v", windowStarts)
	}
}

func makeFixedGroup(prefix string, size int) group {
	g := make(group, size)
	for i := 0; i < size; i++ {
		g[i] = sqFlat{ID: fmt.Sprintf("%s-%d", prefix, i)}
	}
	return g
}

// Regression: greedyFill (no RNG) must still behave deterministically —
// callers that rely on the old wrapper signature keep the same semantics.
func TestGreedyFillNoRNGDeterministic(t *testing.T) {
	pool := []group{makeFixedGroup("A", 2), makeFixedGroup("B", 2)}
	a := greedyFill(append([]group(nil), pool...), 2)
	b := greedyFill(append([]group(nil), pool...), 2)
	if len(a) != len(b) || a[0][0].ID != b[0][0].ID {
		t.Fatalf("expected identical output without RNG, got %v vs %v", a, b)
	}
}
