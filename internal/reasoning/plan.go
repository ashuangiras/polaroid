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

// PlanEntry is one entry of a Plan (ADR-0052) or PlanTree (ADR-0054): an
// immediate composed child of a goal, in precedes order. Status is
// "done" (named in completed, or every leaf beneath it done), "next"
// (the first not-done entry), "escalate" or "blocked" (PlanTree only: a
// leaf beneath it is frozen there — ADR-0056 distinguishes the two, see
// LeafStatus), or unmarked (the rest, omitted from the JSON member
// entirely). Path is set only by PlanTree, only on the "next",
// "escalate" or "blocked" entry: the chain of keys from this entry down
// to the actual leaf — a single-element array, equal to [Key], when
// this entry is itself a leaf (Plan's entries are always leaves in this
// sense, but never set Path, since resolve has no notion of a frozen
// leaf or of recursing). Children is set only by ExpandTree (ADR-0056's
// expand): this entry's own nested entries, when it is itself composed,
// to any depth; nil otherwise.
type PlanEntry struct {
	Key      string      `json:"key"`
	Status   string      `json:"status,omitzero"`
	Path     []string    `json:"path,omitzero"`
	Children []PlanEntry `json:"children,omitzero"`
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
	records, err := recordsByKey(cat, goal.Composes)
	if err != nil {
		return nil, err
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

// LeafStatus reports what has been recorded for one leaf's full path
// (ADR-0054): Done once a done or skipped outcome was last reported for
// it, Escalate once an escalate was last reported and nothing later
// overrode it. Neither set means nothing has been reported for this
// path yet.
type LeafStatus struct {
	Done     bool
	Escalate bool
	// Blocked is agent-reported testimony that an external, technical
	// precondition is not met (ADR-0056), kept distinct from Escalate so
	// a reader can tell the two apart; Polaroid itself never derives
	// this (internal/reasoning/datalog is open-world: it never infers a
	// fact is false merely because it was not asserted).
	Blocked bool
}

// maxPlanDepth defends PlanTree's recursion against a composes chain
// deeper than ADR-0054 assumes is possible. The ADR says this graph is
// "already cycle-checked and bounded at publish time" the same way the
// legacy reference graph is (knowledge.MaxGraphDepth/MaxGraphNodes); only
// the cycle check actually applies to composes today — publish-time
// validation enforces no depth or node-count bound on it the way it does
// for legacy references (internal/knowledge/validate.go's checkGraph
// walks only Procedure.References). This reuses the ADR's own cited
// number as a practical ceiling without asserting it is enforced
// upstream; restoring that enforcement is a separate, narrower gap.
const maxPlanDepth = knowledge.MaxGraphDepth

// ErrPlanTooDeep reports a composes chain deeper than maxPlanDepth: a
// catalog inconsistency (composes's own cycle check should make this
// unreachable in practice, absent the missing depth bound noted above),
// never a caller mistake.
var ErrPlanTooDeep = errors.New("composition is deeper than expected")

// PlanTree resolves ADR-0054's multi-level plan for goal: the same
// one-level shape as Plan — one entry per immediate composed child, in
// precedes order — but a composed child's status is always derived from
// its own descendants (recursing through composes with the same
// precedes ordering at every level), never read directly: "done" once
// every leaf beneath it is done, "escalate" if a leaf beneath it is
// frozen there. The first not-done entry's Path gives the full chain of
// keys from it down to the actual next (or frozen) leaf: a single-
// element path when that entry is itself a leaf, exactly Plan's shape.
// leafStatus is called only for actual leaves, keyed by their full path
// from goal (not including goal itself); a composed node's status is
// purely structural and never queries it. PlanTree returns nil, nil when
// goal has no composed children, exactly like Plan.
//
// Unlike Plan (used by the stateless resolve, unaffected by this
// addition), an immediate composed child here is never itself directly
// markable done: only persisted, path-addressed state can supply
// leafStatus, so PlanTree is used only by a stateful caller (ADR-0053's
// walkthroughs, through internal/knowledgeops.Guide).
func PlanTree(cat *knowledge.Catalog, goal *knowledge.Record, leafStatus func(path []string) LeafStatus) ([]PlanEntry, error) {
	if len(goal.Composes) == 0 {
		return nil, nil
	}
	records, err := recordsByKey(cat, goal.Composes)
	if err != nil {
		return nil, err
	}
	order := topoSortByPrecedes(records, goal.Composes)
	out := make([]PlanEntry, len(order))
	active := false
	for i, k := range order {
		if active {
			out[i] = PlanEntry{Key: k}
			continue
		}
		done, frozen, below, err := planSubtree(cat, []string{k}, records[k], leafStatus, 1)
		if err != nil {
			return nil, err
		}
		if done {
			out[i] = PlanEntry{Key: k, Status: "done"}
			continue
		}
		active = true
		status := "next"
		if frozen != "" {
			status = frozen
		}
		out[i] = PlanEntry{Key: k, Status: status, Path: append([]string{k}, below...)}
	}
	return out, nil
}

// planSubtree reports whether every leaf under rec (reached at path from
// goal) is done, whether an escalated or blocked leaf freezes it
// ("escalate" or "blocked"; "" if nothing freezes it), and, when not
// done, the chain of keys strictly below rec down to that leaf (nil for
// a leaf itself, which has nothing below it).
func planSubtree(cat *knowledge.Catalog, path []string, rec *knowledge.Record, leafStatus func([]string) LeafStatus, depth int) (done bool, frozen string, below []string, err error) {
	if depth > maxPlanDepth {
		return false, "", nil, ErrPlanTooDeep
	}
	if len(rec.Composes) == 0 {
		st := leafStatus(path)
		switch {
		case st.Done:
			return true, "", nil, nil
		case st.Escalate:
			return false, "escalate", nil, nil
		case st.Blocked:
			return false, "blocked", nil, nil
		default:
			return false, "", nil, nil
		}
	}
	children, err := recordsByKey(cat, rec.Composes)
	if err != nil {
		return false, "", nil, err
	}
	for _, k := range topoSortByPrecedes(children, rec.Composes) {
		cdone, cfrozen, cbelow, err := planSubtree(cat, slices.Concat(path, []string{k}), children[k], leafStatus, depth+1)
		if err != nil {
			return false, "", nil, err
		}
		if cdone {
			continue
		}
		return false, cfrozen, append([]string{k}, cbelow...), nil
	}
	return true, "", nil, nil
}

// ExpandTree resolves goal's full, nested plan tree (ADR-0056's expand):
// the same top-level next/path/escalate/blocked PlanTree already gives
// (identical values, since PlanTree is called directly for them), but
// every composed entry additionally carries its own Children — its own
// nested entries, recursively, to any depth, safe without a new bound
// for the same reason PlanTree's own recursion already is (the composes
// graph is finite; maxPlanDepth defends against the residual gap noted
// on that constant). A nested entry's own status uses a fourth value,
// "pending" (nothing reported for it or any of its descendants),
// instead of being omitted: unlike the top level, a nested entry's
// status is not gated by its siblings' order — expand's whole purpose is
// seeing the real state of everything at once, not discovering it one
// leaf at a time — so, unlike the top level, more than one nested
// leaf's escalate or blocked can be visible at once, in any position.
func ExpandTree(cat *knowledge.Catalog, goal *knowledge.Record, leafStatus func(path []string) LeafStatus) ([]PlanEntry, error) {
	plan, err := PlanTree(cat, goal, leafStatus)
	if err != nil || plan == nil {
		return plan, err
	}
	for i := range plan {
		rec, err := cat.Record(plan[i].Key)
		if err != nil {
			return nil, err
		}
		if len(rec.Composes) == 0 {
			continue
		}
		children, err := buildChildren(cat, []string{plan[i].Key}, rec.Composes, leafStatus, 2)
		if err != nil {
			return nil, err
		}
		plan[i].Children = children
	}
	return plan, nil
}

// buildChildren resolves composes's own entries (ADR-0056's expand),
// each with its true status — "done", "escalate", "blocked" or
// "pending" — independent of its siblings' position (unlike PlanTree's
// own "only the first not-done one is revealed" rule), and Children
// attached recursively when an entry is itself composed.
func buildChildren(cat *knowledge.Catalog, path []string, composes []string, leafStatus func([]string) LeafStatus, depth int) ([]PlanEntry, error) {
	if depth > maxPlanDepth {
		return nil, ErrPlanTooDeep
	}
	records, err := recordsByKey(cat, composes)
	if err != nil {
		return nil, err
	}
	order := topoSortByPrecedes(records, composes)
	out := make([]PlanEntry, len(order))
	for i, k := range order {
		childPath := slices.Concat(path, []string{k})
		rec := records[k]
		entry := PlanEntry{Key: k}
		if len(rec.Composes) == 0 {
			st := leafStatus(childPath)
			switch {
			case st.Done:
				entry.Status = "done"
			case st.Escalate:
				entry.Status = "escalate"
			case st.Blocked:
				entry.Status = "blocked"
			default:
				entry.Status = "pending"
			}
		} else {
			children, err := buildChildren(cat, childPath, rec.Composes, leafStatus, depth+1)
			if err != nil {
				return nil, err
			}
			entry.Children = children
			entry.Status = summarizeChildren(children)
		}
		out[i] = entry
	}
	return out, nil
}

// summarizeChildren derives a composed entry's own status from its
// already-built children (ADR-0056's expand): "done" iff every one is
// done; else "escalate" if any one is (prioritized, since it means a
// person's judgment is needed); else "blocked" if any one is; else
// "pending". Unlike planSubtree's own rollup, this is not limited to the
// first not-done child: expand means showing the real state of
// everything beneath, not only what strict precedes order has reached.
func summarizeChildren(children []PlanEntry) string {
	allDone, anyBlocked := true, false
	for _, c := range children {
		if c.Status != "done" {
			allDone = false
		}
		switch c.Status {
		case "escalate":
			return "escalate"
		case "blocked":
			anyBlocked = true
		}
	}
	switch {
	case allDone:
		return "done"
	case anyBlocked:
		return "blocked"
	default:
		return "pending"
	}
}

// recordsByKey fetches each of keys as a typed record, by key.
func recordsByKey(cat *knowledge.Catalog, keys []string) (map[string]*knowledge.Record, error) {
	records := make(map[string]*knowledge.Record, len(keys))
	for _, k := range keys {
		r, err := cat.Record(k)
		if err != nil {
			return nil, err
		}
		records[k] = r
	}
	return records, nil
}
