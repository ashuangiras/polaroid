package reasoning

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// ErrInvalidCompleted: a name in Plan's completed argument is not one of
// the goal's immediate composed children.
var ErrInvalidCompleted = errors.New("completed names a key that is not one of the goal's immediate composed children")

// PlanEntry is one entry of a Plan (ADR-0052): an immediate composed
// child of a goal, in precedes order. Status is "done" (named in
// completed), "next" (the first not-done entry), or unmarked (the rest,
// omitted from the JSON member entirely).
type PlanEntry struct {
	Key    string `json:"key"`
	Status string `json:"status,omitzero"`
}

// Plan resolves ADR-0052's ordered sibling plan for goal: its immediate
// composed children (one level, not recursive — a composed child that is
// itself composed is resolved separately, exactly as read's references
// resolve only one level), topologically ordered by their precedes edges
// (ties broken by key, deterministically), each marked "done" if named in
// completed, or "next" for the first not-done one. Plan returns nil, nil
// when goal has no composed children: there is nothing to plan, not an
// error. A name in completed that is not one of goal's immediate composed
// children is ErrInvalidCompleted.
func Plan(cat *knowledge.Catalog, goal *knowledge.Record, completed []string) ([]PlanEntry, error) {
	if len(goal.Composes) == 0 {
		return nil, nil
	}
	done := map[string]bool{}
	for _, k := range completed {
		if !slices.Contains(goal.Composes, k) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidCompleted, k)
		}
		done[k] = true
	}
	records := map[string]*knowledge.Record{}
	for _, k := range goal.Composes {
		r, err := cat.Record(k)
		if err != nil {
			return nil, err
		}
		records[k] = r
	}
	order := topoSortByPrecedes(records, goal.Composes)
	out := make([]PlanEntry, len(order))
	foundNext := false
	for i, k := range order {
		switch {
		case done[k]:
			out[i] = PlanEntry{Key: k, Status: "done"}
		case !foundNext:
			out[i] = PlanEntry{Key: k, Status: "next"}
			foundNext = true
		default:
			out[i] = PlanEntry{Key: k}
		}
	}
	return out, nil
}

// topoSortByPrecedes orders keys by their precedes edges (restricted to
// records, the sibling set itself): a topological sort, ties broken by
// key so the result is deterministic. Publish-time validation already
// guarantees this graph is acyclic and every precedes target is a
// sibling (ADR-0052), so this never reports a cycle itself; a residual
// one (data from an older, less strict validator) just stops ordering
// early, and the remaining keys are appended in key order.
func topoSortByPrecedes(records map[string]*knowledge.Record, keys []string) []string {
	nodes := slices.Clone(keys)
	slices.Sort(nodes)
	indegree := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
	known := make(map[string]bool, len(nodes))
	for _, k := range nodes {
		known[k] = true
	}
	for _, k := range nodes {
		for _, t := range records[k].Precedes {
			if known[t] {
				adj[k] = append(adj[k], t)
				indegree[t]++
			}
		}
	}
	var ready []string
	for _, k := range nodes {
		if indegree[k] == 0 {
			ready = append(ready, k)
		}
	}
	slices.Sort(ready)
	var order []string
	for len(ready) > 0 {
		k := ready[0]
		ready = ready[1:]
		order = append(order, k)
		next := slices.Clone(adj[k])
		slices.Sort(next)
		for _, t := range next {
			indegree[t]--
			if indegree[t] == 0 {
				i, _ := slices.BinarySearch(ready, t)
				ready = slices.Insert(ready, i, t)
			}
		}
	}
	// A residual cycle (should not happen: publish-time validation
	// refuses one) leaves some nodes with indegree > 0 forever; append
	// them in key order rather than silently dropping them.
	if len(order) != len(nodes) {
		for _, k := range nodes {
			if !slices.Contains(order, k) {
				order = append(order, k)
			}
		}
	}
	return order
}
