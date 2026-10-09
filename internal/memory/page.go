package memory

import (
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"time"
)

// MaxPageLimit is the largest page a list returns (ADR-0021).
const MaxPageLimit = 500

// Page asks for at most Limit items after the cursor After, which a previous
// page returned as its next cursor. A zero Limit asks for the complete list,
// as before pagination existed; After then must be empty.
type Page struct {
	Limit int
	After string
}

// Position is where a page continues: after the item with this sort key.
// Key-ordered lists use Key (and ID to break ties); time-ordered lists use At
// and ID.
type Position struct {
	Key string
	At  time.Time
	ID  string
}

func (p Page) check(prob *problems) bool {
	switch {
	case p.Limit < 0 || p.Limit > MaxPageLimit:
		prob.add("limit", fmt.Sprintf("must be a number from 1 to %d", MaxPageLimit))
	case p.Limit == 0 && p.After != "":
		prob.add("after", "requires limit")
		return false
	}
	return true
}

// cursor parts, decoded, or nil if after is not a cursor with n parts.
func cursorParts(after string, n int) []string {
	raw, err := base64.RawURLEncoding.DecodeString(after)
	if err != nil {
		return nil
	}
	var parts []string
	if json.Unmarshal(raw, &parts) != nil || len(parts) != n {
		return nil
	}
	return parts
}

func encodeCursor(parts ...string) string {
	raw, err := json.Marshal(parts)
	if err != nil {
		panic(err) // a []string always encodes
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func timeCursor(at time.Time, id string) string {
	return encodeCursor(at.UTC().Format(time.RFC3339Nano), id)
}

func keyCursor(key, id string) string {
	return encodeCursor(key, id)
}

const badCursor = "must be the next cursor of an earlier page"

// timePosition checks p and returns its position in a time-ordered list.
func (p Page) timePosition(prob *problems) *Position {
	if !p.check(prob) || p.After == "" {
		return nil
	}
	parts := cursorParts(p.After, 2)
	if parts == nil {
		prob.add("after", badCursor)
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil || parts[1] == "" {
		prob.add("after", badCursor)
		return nil
	}
	return &Position{At: at.UTC(), ID: parts[1]}
}

// keyPosition checks p and returns its position in a key-ordered list.
func (p Page) keyPosition(prob *problems) *Position {
	if !p.check(prob) || p.After == "" {
		return nil
	}
	parts := cursorParts(p.After, 2)
	if parts == nil || parts[0] == "" {
		prob.add("after", badCursor)
		return nil
	}
	return &Position{Key: parts[0], ID: parts[1]}
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
