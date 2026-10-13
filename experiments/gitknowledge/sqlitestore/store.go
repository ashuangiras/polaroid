// Package sqlitestore is the reference adapter: living knowledge on SQLite,
// with the same driver and durability settings as Polaroid's production
// store, and none of its version, binding or execution model.
package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
)

const schema = `
CREATE TABLE IF NOT EXISTS blobs (id INTEGER PRIMARY KEY, hash TEXT NOT NULL UNIQUE, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS snapshots (
  seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, parent TEXT,
  author TEXT NOT NULL, message TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS changes (
  key TEXT NOT NULL, seq INTEGER NOT NULL REFERENCES snapshots(seq),
  hash TEXT REFERENCES blobs(hash), PRIMARY KEY (key, seq)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS changes_seq ON changes(seq);
CREATE TABLE IF NOT EXISTS head (one INTEGER PRIMARY KEY CHECK (one = 1), seq INTEGER NOT NULL);
CREATE TRIGGER IF NOT EXISTS blobs_immutable BEFORE UPDATE ON blobs BEGIN SELECT RAISE(ABORT, 'immutable'); END;
CREATE TRIGGER IF NOT EXISTS blobs_kept BEFORE DELETE ON blobs BEGIN SELECT RAISE(ABORT, 'immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshots_immutable BEFORE UPDATE ON snapshots BEGIN SELECT RAISE(ABORT, 'immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshots_kept BEFORE DELETE ON snapshots BEGIN SELECT RAISE(ABORT, 'immutable'); END;
CREATE TRIGGER IF NOT EXISTS changes_immutable BEFORE UPDATE ON changes BEGIN SELECT RAISE(ABORT, 'immutable'); END;
CREATE TRIGGER IF NOT EXISTS changes_kept BEFORE DELETE ON changes BEGIN SELECT RAISE(ABORT, 'immutable'); END;
`

// Store keeps every snapshot as the set of changes it made. History is
// linear: a snapshot's state is the latest change of each key at or below
// its sequence number, and head names the active one.
type Store struct {
	db    *sql.DB
	pol   knowledge.Policy
	cache *knowledge.Cache
	mu    sync.Mutex
	manif map[string]map[string]string // snapshot -> key -> hash, immutable
	seqOf map[string]int64
}

// Open opens or creates the store with Polaroid's production settings;
// unsafeNoSync (synchronous=off) exists only to measure what durability costs.
func Open(path string, pol knowledge.Policy, unsafeNoSync ...bool) (*Store, error) {
	sync := "full"
	if len(unsafeNoSync) > 0 && unsafeNoSync[0] {
		sync = "off"
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_busy_timeout=5000&_foreign_keys=on&_journal_mode=wal&_synchronous="+sync)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, pol: pol, cache: knowledge.NewCache(1 << 16), manif: map[string]map[string]string{}, seqOf: map[string]int64{}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Head(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT s.id FROM head h JOIN snapshots s ON s.seq = h.seq`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) seq(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, snapshot string) (int64, error) {
	s.mu.Lock()
	n, ok := s.seqOf[snapshot]
	s.mu.Unlock()
	if ok {
		return n, nil
	}
	err := q.QueryRowContext(ctx, `SELECT seq FROM snapshots WHERE id = ?`, snapshot).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("snapshot %s: %w", snapshot, knowledge.ErrNotFound)
	}
	if err == nil {
		s.mu.Lock()
		s.seqOf[snapshot] = n
		s.mu.Unlock()
	}
	return n, err
}

func (s *Store) ReadActive(ctx context.Context, key string) (knowledge.Knowledge, error) {
	head, err := s.Head(ctx)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	if head == "" {
		return knowledge.Knowledge{}, fmt.Errorf("%s: %w", key, knowledge.ErrNotFound)
	}
	return s.ReadAt(ctx, key, head)
}

func (s *Store) ReadAt(ctx context.Context, key, snapshot string) (knowledge.Knowledge, error) {
	m, err := s.manifest(ctx, snapshot)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	hash, ok := m[key]
	if !ok {
		return knowledge.Knowledge{}, fmt.Errorf("%s at %s: %w", key, snapshot, knowledge.ErrNotFound)
	}
	p, err := s.load(ctx)(key, hash)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	return knowledge.Knowledge{Key: key, Snapshot: snapshot, Hash: hash, Procedure: p}, nil
}

func (s *Store) decode(hash string, body []byte) (*knowledge.Procedure, error) {
	if c, ok := s.cache.Get(hash); ok {
		return c.Procedure, nil
	}
	p, err := knowledge.Decode(body, 0)
	if err != nil {
		return nil, fmt.Errorf("stored document %s: %w", hash, err)
	}
	s.cache.Put(hash, knowledge.Cached{Procedure: p, Hash: hash})
	return p, nil
}

func (s *Store) manifest(ctx context.Context, snapshot string) (map[string]string, error) {
	s.mu.Lock()
	m, ok := s.manif[snapshot]
	s.mu.Unlock()
	if ok {
		return m, nil
	}
	seq, err := s.seq(ctx, s.db, snapshot)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, hash FROM (
		SELECT key, hash, row_number() OVER (PARTITION BY key ORDER BY seq DESC) AS rn FROM changes WHERE seq <= ?)
		WHERE rn = 1 AND hash IS NOT NULL`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m = map[string]string{}
	for rows.Next() {
		var k, h string
		if err := rows.Scan(&k, &h); err != nil {
			return nil, err
		}
		m[k] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.manif) > 8 {
		clear(s.manif)
	}
	s.manif[snapshot] = m
	s.mu.Unlock()
	return m, nil
}

func (s *Store) Keys(ctx context.Context, snapshot string) ([]string, error) {
	m, err := s.manifest(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *Store) load(ctx context.Context) func(key, hash string) (*knowledge.Procedure, error) {
	return func(key, hash string) (*knowledge.Procedure, error) {
		if c, ok := s.cache.Get(hash); ok {
			return c.Procedure, nil
		}
		var body []byte
		if err := s.db.QueryRowContext(ctx, `SELECT body FROM blobs WHERE hash = ?`, hash).Scan(&body); err != nil {
			return nil, err
		}
		return s.decode(hash, body)
	}
}

func (s *Store) Publish(ctx context.Context, expected string, changes []knowledge.Change, note knowledge.Note) (string, error) {
	bodies, err := knowledge.CheckChanges(changes, s.pol)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil) // _txlock=immediate: one writer at a time
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var headSeq int64
	var head string
	err = tx.QueryRowContext(ctx, `SELECT h.seq, s.id FROM head h JOIN snapshots s ON s.seq = h.seq`).Scan(&headSeq, &head)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if head != expected {
		return "", knowledge.ErrConflict
	}
	base := map[string]string{}
	if head != "" {
		if base, err = s.manifest(ctx, head); err != nil {
			return "", err
		}
	}
	if err := knowledge.Validate(&knowledge.Overlay{Base: base, Load: s.load(ctx), Changes: changes}, s.pol); err != nil {
		return "", err
	}
	if note.Time.IsZero() {
		note.Time = time.Now().UTC()
	}
	id := snapshotID(head, changes, bodies, note)
	seq := headSeq + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots (seq, id, parent, author, message, created_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?)`,
		seq, id, head, note.Author, note.Message, note.Time.Format(time.RFC3339Nano)); err != nil {
		return "", err
	}
	for i, c := range changes {
		var hash any
		if c.Procedure != nil {
			h := knowledge.Hash(bodies[i])
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blobs (hash, body) VALUES (?, ?)`, h, bodies[i]); err != nil {
				return "", err
			}
			hash = h
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO changes (key, seq, hash) VALUES (?, ?, ?)`, c.Key, seq, hash); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO head (one, seq) VALUES (1, ?) ON CONFLICT (one) DO UPDATE SET seq = excluded.seq`, seq); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	next := make(map[string]string, len(base)+len(changes))
	for k, h := range base {
		next[k] = h
	}
	for i, c := range changes {
		if c.Procedure != nil {
			next[c.Key] = knowledge.Hash(bodies[i])
		} else {
			delete(next, c.Key)
		}
	}
	s.mu.Lock()
	if len(s.manif) > 8 {
		clear(s.manif)
	}
	s.manif[id] = next
	s.seqOf[id] = seq
	s.mu.Unlock()
	return id, nil
}

func snapshotID(parent string, changes []knowledge.Change, bodies [][]byte, note knowledge.Note) string {
	h := sha256.New()
	fmt.Fprintf(h, "parent %s\nauthor %s\ntime %s\n%s\n", parent, note.Author, note.Time.Format(time.RFC3339Nano), note.Message)
	for i, c := range changes {
		fmt.Fprintf(h, "%s %x\n", c.Key, sha256.Sum256(bodies[i]))
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
}

func (s *Store) History(ctx context.Context, limit int) ([]knowledge.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.seq, s.id, coalesce(s.parent, ''), s.author, s.message, s.created_at,
		(SELECT group_concat(key, ',') FROM (SELECT key FROM changes WHERE seq = s.seq ORDER BY key))
		FROM snapshots s, head h WHERE s.seq <= h.seq ORDER BY s.seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.Entry
	for rows.Next() {
		var seq int64
		var e knowledge.Entry
		var parent, at string
		var changed sql.NullString
		if err := rows.Scan(&seq, &e.Snapshot, &parent, &e.Note.Author, &e.Note.Message, &at, &changed); err != nil {
			return nil, err
		}
		if parent != "" {
			e.Parents = []string{parent}
		}
		e.Note.Time, _ = time.Parse(time.RFC3339Nano, at)
		if changed.Valid {
			e.Changed = strings.Split(changed.String, ",")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Backup copies the database consistently (VACUUM INTO), the SQLite
// equivalent of mirroring the Git repository.
func (s *Store) Backup(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
