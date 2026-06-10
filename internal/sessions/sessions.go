// Package sessions persists Q&A history (§5 qa_session + qa_message). It writes
// no vectors; the ask path calls EnsureSession + AppendPair, the API reads
// ListByKB + Transcript. kb isolation is enforced by joining qa_message→
// qa_session and filtering on kb_id.
package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a session does not exist for the kb.
var ErrNotFound = errors.New("sessions: not found")

// Repo persists sessions + messages.
type Repo struct{ pool *pgxpool.Pool }

// New builds a sessions Repo.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Session is one qa_session row.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"createdAt"`
}

// Message is one qa_message row.
type Message struct {
	ID            string `json:"id"`
	Role          string `json:"role"`
	Content       string `json:"content"`
	CitationsJSON []byte `json:"-"`
	Mode          string `json:"mode"`
	CreatedAt     string `json:"createdAt"`
}

// EnsureSession returns sessionID when non-empty AND it belongs to kb+user;
// otherwise it creates a new session titled from the first question (trimmed).
// An inbound sessionID that does not match kb+user is treated as "create new"
// (no cross-tenant write).
func (r *Repo) EnsureSession(ctx context.Context, kbID, userID, sessionID, firstQuestion string) (string, error) {
	if sessionID != "" {
		var owned bool
		err := r.pool.QueryRow(ctx,
			`SELECT true FROM qa_session WHERE id = $1 AND kb_id = $2 AND user_id = $3`,
			sessionID, kbID, userID).Scan(&owned)
		if err == nil && owned {
			return sessionID, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		// not owned → fall through to create
	}
	id := newID()
	title := firstQuestion
	if len(title) > 80 {
		title = title[:80]
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO qa_session (id, kb_id, user_id, title) VALUES ($1, $2, $3, $4)`,
		id, kbID, userID, title); err != nil {
		return "", fmt.Errorf("sessions: create: %w", err)
	}
	return id, nil
}

// AppendPair writes the user question + assistant answer as two qa_message rows
// in one transaction. citationsJSON is the serialized citation array (assistant
// row); mode is the ask mode (vector/hybrid/global/drift).
func (r *Repo) AppendPair(ctx context.Context, sessionID, question, answer string, citationsJSON []byte, mode string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`INSERT INTO qa_message (id, session_id, role, content, citations_json, mode)
		 VALUES ($1, $2, 'user', $3, NULL, $4)`,
		newID(), sessionID, question, mode); err != nil {
		return fmt.Errorf("sessions: append user msg: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO qa_message (id, session_id, role, content, citations_json, mode)
		 VALUES ($1, $2, 'assistant', $3, $4, $5)`,
		newID(), sessionID, answer, citationsJSON, mode); err != nil {
		return fmt.Errorf("sessions: append assistant msg: %w", err)
	}
	return tx.Commit(ctx)
}

// ListByKB returns up to limit sessions for a kb, newest first, keyset-paginated
// on id (cursor = last id seen).
func (r *Repo) ListByKB(ctx context.Context, kbID string, limit int, cursor string) ([]Session, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, title, created_at::text FROM qa_session
		 WHERE kb_id = $1 AND ($2 = '' OR id < $2)
		 ORDER BY id DESC LIMIT $3`, kbID, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Title, &s.CreatedAt); err != nil {
			return nil, "", err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// Transcript returns the messages of a session in chronological order, after
// verifying the session belongs to kbID (cross-tenant read → ErrNotFound).
func (r *Repo) Transcript(ctx context.Context, kbID, sessionID string) ([]Message, error) {
	var owned bool
	err := r.pool.QueryRow(ctx,
		`SELECT true FROM qa_session WHERE id = $1 AND kb_id = $2`, sessionID, kbID).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, role, content, citations_json, mode, created_at::text
		 FROM qa_message WHERE session_id = $1 ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var cites []byte
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &cites, &m.Mode, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.CitationsJSON = cites
		out = append(out, m)
	}
	return out, rows.Err()
}
