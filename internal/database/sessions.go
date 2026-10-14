package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DeleteSessions removes the sessions rows for sessionIDs. Called alongside
// DeleteExchanges only when the caller also purges those sessions' on-disk
// Claude Code files, since a session row is otherwise independent of the
// soft-deleted exchange log.
func (db *DB) DeleteSessions(ctx context.Context, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	placeholders, args := sqlInClause(sessionIDs)
	_, err := db.sql.ExecContext(ctx, "DELETE FROM sessions WHERE session_id IN ("+placeholders+")", args...)
	return err
}

// SessionName returns a session's display name and whether the session
// exists.
func (db *DB) SessionName(ctx context.Context, sessionID string) (name string, ok bool, err error) {
	err = db.sql.QueryRowContext(ctx, "SELECT name FROM sessions WHERE session_id = ?", sessionID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// SetSessionName sets a session's display name, creating the session row if
// needed. An empty (after trimming) name resets the display name to the
// session id.
func (db *DB) SetSessionName(ctx context.Context, sessionID, name string) error {
	if sessionID == "" {
		return errors.New("session_id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = sessionID
	}
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO sessions (session_id, name, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
		sessionID, name, float64(time.Now().Unix()),
	)
	return err
}
