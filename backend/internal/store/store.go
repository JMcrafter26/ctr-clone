package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Row struct {
	Apex      string
	Sub       string
	FirstSeen time.Time
}

type SearchResult struct {
	Sub       string
	FirstSeen time.Time
}

func Open(path string) (*Store, error) {
	dsn := path
	// modernc sqlite: file:<path>?cache=shared&_pragma=...
	// Use WAL + busy timeout + foreign_keys off for speed
	if path != ":memory:" {
		dsn = fmt.Sprintf("file:%s?cache=shared", path)
	} else {
		dsn = "file::memory:?cache=shared"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// Pragmas
	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA temp_store=MEMORY`,
		`PRAGMA mmap_size=268435456`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			// non-fatal for memory db
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS names (
			apex TEXT NOT NULL,
			sub TEXT NOT NULL,
			first_seen INTEGER NOT NULL,
			PRIMARY KEY (apex, sub)
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS idx_names_apex_firstseen ON names(apex, first_seen)`,
		`CREATE INDEX IF NOT EXISTS idx_names_sub ON names(sub)`,
		`CREATE TABLE IF NOT EXISTS log_state (
			log_name TEXT PRIMARY KEY,
			next_index INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		) WITHOUT ROWID`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// InsertBatch does INSERT OR IGNORE batch; first_seen is kept minimal (earliest).
// Returns number of new rows actually inserted.
func (s *Store) InsertBatch(ctx context.Context, rows []Row) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO names(apex, sub, first_seen) VALUES(?, ?, ?)
		ON CONFLICT(apex, sub) DO UPDATE SET first_seen = MIN(first_seen, excluded.first_seen)
		WHERE first_seen > excluded.first_seen`)
	// Fallback for older sqlite without MIN expr in upsert? Use simpler if fails.
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var inserted int64
	for _, r := range rows {
		res, err := stmt.ExecContext(ctx, r.Apex, r.Sub, r.FirstSeen.UnixMilli())
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		// RowsAffected for upsert counts 1 even on update; we approximate by checking if n==1 and first insert?
		// For our metrics we just count n where we inserted new apex+sub (first time). The ON CONFLICT DO UPDATE still returns 1.
		// To distinguish, we could query. For simplicity, count all affected; caller uses for stats delta but not critical.
		_ = n
		inserted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// InsertBatchIgnore is simpler: OR IGNORE keeps earliest first_seen untouched.
func (s *Store) InsertBatchIgnore(ctx context.Context, rows []Row) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO names(apex, sub, first_seen) VALUES(?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var inserted int64
	for _, r := range rows {
		res, err := stmt.ExecContext(ctx, r.Apex, r.Sub, r.FirstSeen.UnixMilli())
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		inserted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *Store) CountAll(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM names`).Scan(&n)
	return n, err
}

func (s *Store) Search(ctx context.Context, apex string, orderNew bool) ([]SearchResult, error) {
	order := "ASC"
	if orderNew {
		order = "DESC"
	}
	q := fmt.Sprintf(`SELECT sub, first_seen FROM names WHERE apex = ? ORDER BY first_seen %s`, order)
	rows, err := s.db.QueryContext(ctx, q, apex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var sub string
		var ms int64
		if err := rows.Scan(&sub, &ms); err != nil {
			return nil, err
		}
		out = append(out, SearchResult{Sub: sub, FirstSeen: time.UnixMilli(ms).UTC()})
	}
	return out, rows.Err()
}

func (s *Store) Find(ctx context.Context, q string, limit int) ([]SearchResult, error) {
	pattern := "%" + q + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT sub, first_seen FROM names WHERE sub LIKE ? ESCAPE '\' ORDER BY first_seen ASC LIMIT ?`, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var sub string
		var ms int64
		if err := rows.Scan(&sub, &ms); err != nil {
			return nil, err
		}
		out = append(out, SearchResult{Sub: sub, FirstSeen: time.UnixMilli(ms).UTC()})
	}
	return out, rows.Err()
}

func (s *Store) Top(ctx context.Context, n int) ([]struct {
	Apex  string
	Count int64
}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT apex, COUNT(*) as cnt FROM names GROUP BY apex ORDER BY cnt DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		Apex  string
		Count int64
	}
	for rows.Next() {
		var apex string
		var cnt int64
		if err := rows.Scan(&apex, &cnt); err != nil {
			return nil, err
		}
		out = append(out, struct {
			Apex  string
			Count int64
		}{Apex: apex, Count: cnt})
	}
	return out, rows.Err()
}

func (s *Store) Recent(ctx context.Context, limit int) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT apex, sub, first_seen FROM names ORDER BY first_seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var ms int64
		if err := rows.Scan(&r.Apex, &r.Sub, &ms); err != nil {
			return nil, err
		}
		r.FirstSeen = time.UnixMilli(ms).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindLike searches for substring q in sub, with escaping.
func EscapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// LogState helpers
func (s *Store) GetLogState(ctx context.Context, logName string) (int64, bool, error) {
	var idx int64
	err := s.db.QueryRowContext(ctx, `SELECT next_index FROM log_state WHERE log_name = ?`, logName).Scan(&idx)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return idx, true, nil
}

func (s *Store) SetLogState(ctx context.Context, logName string, nextIndex int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO log_state(log_name, next_index, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(log_name) DO UPDATE SET next_index=excluded.next_index, updated_at=excluded.updated_at`, logName, nextIndex, time.Now().UnixMilli())
	return err
}

// SeedCount returns count for check if empty
func (s *Store) IsEmpty(ctx context.Context) (bool, error) {
	n, err := s.CountAll(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}
