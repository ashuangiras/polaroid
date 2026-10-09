package memory

import (
	"crypto/sha256"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxPageLimit is the largest page a list returns (ADR-0021).
const MaxPageLimit = 500

// Page asks for at most Limit items after the cursor After, which a previous
// page of the same list and parameters returned as its next cursor. A zero
// Limit asks for the complete list, as before pagination existed; After then
// must be empty. Snapshot, on the lists that offer it, pages a snapshot
// fixed at the first page (ADR-0023); it requires Limit.
type Page struct {
	Limit    int
	After    string
	Snapshot bool
}

// Position is where a page continues: after the item with this sort key.
// Key-ordered lists use Key (and ID to break ties); time-ordered lists use At
// and ID.
type Position struct {
	Key string
	At  time.Time
	ID  string
}

// Snapshot is the boundary of a snapshot traversal: the store's high-water
// marks of the append-only tables a list reads, opaque to this package. Nil
// means a live read.
type Snapshot []int64

// ErrInvalidSnapshot is returned by a store given a snapshot it did not
// issue for that list.
var ErrInvalidSnapshot = errors.New("invalid snapshot boundary")

// cursor is the content of a next cursor. Filter is a digest of the list's
// name and of every parameter that selects its items, snapshot included
// (ADR-0023).
type cursor struct {
	Filter   string   `json:"f"`
	Key      string   `json:"k"`
	ID       string   `json:"i"`
	Snapshot Snapshot `json:"s,omitzero"`
}

// pager continues one list with one set of parameters.
type pager struct {
	page   Page
	filter string
	timed  bool
	// Snapshot is the traversal's boundary: from the cursor, or set by the
	// service from the store on a first page.
	Snapshot Snapshot
}

// newPager returns a pager for list, a time-ordered one if timed, whose
// items params select.
func newPager(list string, timed bool, page Page, params ...string) *pager {
	h := sha256.New()
	h.Write([]byte(list))
	for _, p := range params {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	if page.Snapshot {
		h.Write([]byte("\x00snapshot"))
	}
	return &pager{page: page, filter: base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:12]), timed: timed}
}

const (
	badCursor   = "must be the next cursor of an earlier page"
	otherCursor = "is the next cursor of another list or of other parameters: repeat the first page's parameters, or start again without after"
)

// start checks the page and returns the position to continue after, nil on
// a first page. A snapshot cursor also sets g.Snapshot.
func (g *pager) start(prob *problems) *Position {
	p := g.page
	switch {
	case p.Limit < 0 || p.Limit > MaxPageLimit:
		prob.add("limit", fmt.Sprintf("must be a number from 1 to %d", MaxPageLimit))
		return nil
	case p.Limit == 0 && p.After != "":
		prob.add("after", "requires limit")
		return nil
	case p.Limit == 0 && p.Snapshot:
		prob.add("snapshot", "requires limit")
		return nil
	case p.Snapshot && g.timed:
		prob.add("snapshot", "is offered only by the procedure and binding lists")
		return nil
	case p.After == "":
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(p.After)
	if err != nil {
		prob.add("after", badCursor)
		return nil
	}
	var c cursor
	if strings.HasPrefix(string(raw), "[") {
		// A cursor issued before ADR-0023 is a bare position, accepted for a
		// live traversal without the parameter check.
		var parts []string
		if json.Unmarshal(raw, &parts) != nil || len(parts) != 2 {
			prob.add("after", badCursor)
			return nil
		}
		if p.Snapshot {
			prob.add("after", otherCursor)
			return nil
		}
		c = cursor{Filter: g.filter, Key: parts[0], ID: parts[1]}
	} else if json.Unmarshal(raw, &c) != nil {
		prob.add("after", badCursor)
		return nil
	}
	if c.Filter != g.filter || p.Snapshot != (c.Snapshot != nil) {
		prob.add("after", otherCursor)
		return nil
	}
	pos := &Position{ID: c.ID}
	if g.timed {
		at, err := time.Parse(time.RFC3339Nano, c.Key)
		if err != nil || c.ID == "" {
			prob.add("after", badCursor)
			return nil
		}
		pos.At = at.UTC()
	} else {
		if c.Key == "" {
			prob.add("after", badCursor)
			return nil
		}
		pos.Key = c.Key
	}
	g.Snapshot = c.Snapshot
	return pos
}

// next returns the cursor continuing after the item with key (or at, in a
// time-ordered list) and id.
func (g *pager) next(key string, at time.Time, id string) string {
	if g.timed {
		key = at.UTC().Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(cursor{Filter: g.filter, Key: key, ID: id, Snapshot: g.Snapshot})
	if err != nil {
		panic(err) // a cursor always encodes
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// listError maps a store's ErrInvalidSnapshot to a problem with after, and
// wraps any other error with the list's name.
func listError(list string, err error) error {
	if errors.Is(err, ErrInvalidSnapshot) {
		return &ValidationError{Problems: []FieldProblem{{Field: "after", Message: badCursor}}}
	}
	return fmt.Errorf("list %s: %w", list, err)
}

// trim cuts items, which a store returned with at most one extra item, to
// the page, and returns the next cursor if the extra item shows there is
// more.
func trim[T any](p Page, items []T, cursor func(T) string) ([]T, string) {
	if p.Limit == 0 || len(items) <= p.Limit {
		return items, ""
	}
	items = items[:p.Limit]
	return items, cursor(items[len(items)-1])
}
