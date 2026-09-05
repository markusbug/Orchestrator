// Package store persists session and device metadata in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Session is a persisted session row.
type Session struct {
	ID              string
	Name            string
	Cwd             string
	Cmd             string
	Args            []string
	PID             int
	Status          string
	ExitCode        *int
	ClaudeSessionID string
	CreatedAt       time.Time
	LastOutputAt    time.Time
	Cols            int
	Rows            int
}

// Device is a paired client.
type Device struct {
	ID         string
	Name       string
	PubKey     []byte
	CreatedAt  time.Time
	LastSeenAt time.Time
	Revoked    bool
}

// Store wraps the database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  cwd TEXT NOT NULL,
  cmd TEXT NOT NULL,
  args TEXT NOT NULL,
  pid INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  exit_code INTEGER,
  claude_session_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_output_at INTEGER NOT NULL,
  cols INTEGER NOT NULL DEFAULT 80,
  rows INTEGER NOT NULL DEFAULT 24
);
CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  pubkey BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS recents (
  cwd TEXT PRIMARY KEY,
  used_at INTEGER NOT NULL
);`)
	return err
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMs(v int64) time.Time { return time.UnixMilli(v) }

// PutSession inserts or replaces a session row.
func (s *Store) PutSession(ctx context.Context, sess Session) error {
	args, _ := json.Marshal(sess.Args)
	if sess.Args == nil {
		args = []byte("[]")
	}
	var exit any
	if sess.ExitCode != nil {
		exit = *sess.ExitCode
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO sessions
(id,name,cwd,cmd,args,pid,status,exit_code,claude_session_id,created_at,last_output_at,cols,rows)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sess.ID, sess.Name, sess.Cwd, sess.Cmd, string(args), sess.PID, sess.Status, exit,
		sess.ClaudeSessionID, ms(sess.CreatedAt), ms(sess.LastOutputAt), sess.Cols, sess.Rows)
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO recents(cwd, used_at) VALUES (?,?)`, sess.Cwd, ms(sess.CreatedAt))
	}
	return err
}

// UpdateSessionStatus updates status and exit code.
func (s *Store) UpdateSessionStatus(ctx context.Context, id, status string, exitCode *int) error {
	var exit any
	if exitCode != nil {
		exit = *exitCode
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET status=?, exit_code=? WHERE id=?`, status, exit, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSessionName renames a session.
func (s *Store) UpdateSessionName(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET name=? WHERE id=?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchSession updates last_output_at.
func (s *Store) TouchSession(ctx context.Context, id string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_output_at=? WHERE id=?`, ms(t), id)
	return err
}

// DeleteSession removes a session row.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}

// GetSession loads one session.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,cwd,cmd,args,pid,status,exit_code,claude_session_id,created_at,last_output_at,cols,rows FROM sessions WHERE id=?`, id)
	if err != nil {
		return Session{}, err
	}
	defer rows.Close()
	list, err := scanSessions(rows)
	if err != nil {
		return Session{}, err
	}
	if len(list) == 0 {
		return Session{}, ErrNotFound
	}
	return list[0], nil
}

// ListSessions returns all sessions, newest first.
func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,cwd,cmd,args,pid,status,exit_code,claude_session_id,created_at,last_output_at,cols,rows FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessions(rows)
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	var out []Session
	for rows.Next() {
		var s Session
		var args string
		var exit sql.NullInt64
		var created, last int64
		if err := rows.Scan(&s.ID, &s.Name, &s.Cwd, &s.Cmd, &args, &s.PID, &s.Status, &exit, &s.ClaudeSessionID, &created, &last, &s.Cols, &s.Rows); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(args), &s.Args)
		if exit.Valid {
			v := int(exit.Int64)
			s.ExitCode = &v
		}
		s.CreatedAt = fromMs(created)
		s.LastOutputAt = fromMs(last)
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkAllStale marks every session that is not exited as stale. Called at
// startup because PTY children do not survive the daemon.
func (s *Store) MarkAllStale(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET status='stale', pid=0 WHERE status IN ('running','waiting')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Recents returns recently used working directories, newest first.
func (s *Store) Recents(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cwd FROM recents ORDER BY used_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutDevice inserts a device.
func (s *Store) PutDevice(ctx context.Context, d Device) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO devices(id,name,pubkey,created_at,last_seen_at,revoked) VALUES (?,?,?,?,?,?)`,
		d.ID, d.Name, d.PubKey, ms(d.CreatedAt), ms(d.LastSeenAt), boolInt(d.Revoked))
	return err
}

// GetDevice loads a device by id.
func (s *Store) GetDevice(ctx context.Context, id string) (Device, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,pubkey,created_at,last_seen_at,revoked FROM devices WHERE id=?`, id)
	var d Device
	var created, seen int64
	var revoked int
	if err := row.Scan(&d.ID, &d.Name, &d.PubKey, &created, &seen, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, ErrNotFound
		}
		return Device{}, err
	}
	d.CreatedAt, d.LastSeenAt, d.Revoked = fromMs(created), fromMs(seen), revoked != 0
	return d, nil
}

// ListDevices returns all devices.
func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,pubkey,created_at,last_seen_at,revoked FROM devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var created, seen int64
		var revoked int
		if err := rows.Scan(&d.ID, &d.Name, &d.PubKey, &created, &seen, &revoked); err != nil {
			return nil, err
		}
		d.CreatedAt, d.LastSeenAt, d.Revoked = fromMs(created), fromMs(seen), revoked != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice marks a device revoked.
func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET revoked=1 WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchDevice updates last_seen_at.
func (s *Store) TouchDevice(ctx context.Context, id string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen_at=? WHERE id=?`, ms(t), id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
