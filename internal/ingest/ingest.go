// Package ingest parses uploaded/pasted text into a ragingest.Document and
// imports it synchronously via the RagPort (spec §6, M1 = sync, MD/TXT/paste).
package ingest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	ragingest "github.com/costa92/llm-agent-rag/ingest"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

func checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// idempotencyKey derives a stable dedup key for an enqueue. source_ref (url or
// original filename) identifies the source when present, else the title (paste).
// Including checksum means a changed paste/file body re-enqueues (new key) while
// an identical re-upload collides on the UNIQUE(idempotency_key) index.
func idempotencyKey(kbID, sourceRef, title, cs string) string {
	srcKey := sourceRef
	if srcKey == "" {
		srcKey = title
	}
	sum := sha256.Sum256([]byte(kbID + ":" + srcKey + ":" + cs))
	return "idem:" + hex.EncodeToString(sum[:])
}

// makeDocument builds the ragingest.Document. ID=SourceID=docID drives
// ReplaceSource + per-source delete. doc_id is deliberately NOT in Metadata —
// citation DocID comes from Chunk.DocID (spec §6/§7).
func makeDocument(docID, kbID string, st SourceType, title, content string) ragingest.Document {
	return ragingest.Document{
		ID:       docID,
		SourceID: docID,
		Title:    title,
		Content:  content,
		Checksum: checksum(content),
		Metadata: map[string]any{
			"kb_id":       kbID,
			"source_type": string(st),
		},
	}
}

// Service performs synchronous ingest: write document row → parse → Import →
// mark ready.
type Service struct {
	pool *pgxpool.Pool
	rag  ragsvc.RagPort
}

// New builds an ingest Service.
func New(pool *pgxpool.Pool, rag ragsvc.RagPort) *Service {
	return &Service{pool: pool, rag: rag}
}

// IngestInput is the input to Ingest.
type IngestInput struct {
	KBID       string
	Namespace  string
	Title      string
	SourceRef  string // url for SourceTypeURL; original filename for files
	SourceType SourceType
	Raw        []byte
}

// Enqueue writes document(pending) + ingest_job(pending) in one tx and returns
// the documentId. The worker (worker.go) drains the queue asynchronously.
// Replaces M1's synchronous Ingest (spec §6: 202 + documentId).
func (s *Service) Enqueue(ctx context.Context, in IngestInput) (string, error) {
	docID := newID()
	// For URL sources, the URL lives in SourceRef and Raw is empty; for file/paste
	// the content is in Raw. content_bytes drives the per-kb quota.
	cs := ""
	if in.SourceType != SourceTypeURL {
		cs = checksum(string(in.Raw))
	}
	// Reimport/dedup short-circuit (spec §6, deferred from M1): if a ready
	// document with the same (kb_id, source_ref OR title) already has this exact
	// checksum, skip enqueue and return the existing id. Same-source key = the
	// (kb_id, source_ref) pair for url/file, else (kb_id, title) for paste.
	// Best-effort only: this read runs OUTSIDE the enqueue tx, so two concurrent
	// identical uploads may both miss the check and both enqueue. Acceptable for
	// M2 (worker uses ReplaceSource:true; the duplicate just re-indexes the same
	// source) — not a transactional guarantee.
	if cs != "" {
		var existing string
		err := s.pool.QueryRow(ctx,
			`SELECT id FROM document
			 WHERE kb_id=$1 AND checksum=$2 AND status='ready'
			 AND ((source_ref<>'' AND source_ref=$3) OR (source_ref='' AND title=$4))
			 LIMIT 1`, in.KBID, cs, in.SourceRef, in.Title).Scan(&existing)
		if err == nil && existing != "" {
			return existing, nil // identical content already indexed — no-op
		}
	}
	// idempotency_key collides for concurrent identical enqueues so the
	// UNIQUE(idempotency_key) index gives a real transactional dedup guarantee
	// (the best-effort checksum short-circuit above runs outside the tx and can
	// race). Key = sha256(kb_id : source_key : checksum); source_key is the
	// source_ref (url/file) when present, else the title (paste). For url sources
	// cs is empty, so the key is stable per (kb_id, url) — identical re-enqueues
	// of the same URL still collide.
	idemKey := idempotencyKey(in.KBID, in.SourceRef, in.Title, cs)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`INSERT INTO document (id, kb_id, title, source_type, source_ref, source_id, checksum, status, content_bytes, content)
		 VALUES ($1,$2,$3,$4,$5,$1,$6,'pending',$7,$8)`,
		docID, in.KBID, in.Title, string(in.SourceType), in.SourceRef, cs, int64(len(in.Raw)), in.Raw); err != nil {
		return "", fmt.Errorf("ingest: insert document: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ingest_job (id, document_id, state, idempotency_key)
		 VALUES ($1,$2,'pending',$3)`,
		newID(), docID, idemKey); err != nil {
		// 23505 = unique_violation: an identical enqueue already holds this key.
		// Roll back this doc insert and return the existing document — genuine
		// idempotency rather than a surfaced error.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(ctx)
			var existing string
			if qerr := s.pool.QueryRow(ctx,
				`SELECT document_id FROM ingest_job WHERE idempotency_key=$1`, idemKey).Scan(&existing); qerr == nil && existing != "" {
				return existing, nil
			}
			return "", fmt.Errorf("ingest: insert job: %w", err)
		}
		return "", fmt.Errorf("ingest: insert job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return docID, nil
}

// DocumentStatus reads a single document's status/phase for progress SSE.
func (s *Service) DocumentStatus(ctx context.Context, kbID, docID string) (string, string, int, string, error) {
	var status, phase, errMsg string
	var cc int
	err := s.pool.QueryRow(ctx,
		`SELECT status, phase, chunk_count, error FROM document WHERE id=$1 AND kb_id=$2`,
		docID, kbID).Scan(&status, &phase, &cc, &errMsg)
	return status, phase, cc, errMsg, err
}

// KBContentBytes returns the cumulative content_bytes for a kb (quota accounting).
func (s *Service) KBContentBytes(ctx context.Context, kbID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(content_bytes),0) FROM document WHERE kb_id=$1`, kbID).Scan(&n)
	return n, err
}

// DocumentView is a row of the documents list.
type DocumentView struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	SourceType string `json:"sourceType"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	Error      string `json:"error,omitempty"`
	ChunkCount int    `json:"chunkCount"`
}

// ListDocuments returns up to limit documents for a kb, keyset-paginated by id
// (mirrors orgkb.ListByOrg). Empty cursor starts from the beginning.
func (s *Service) ListDocuments(ctx context.Context, kbID string, limit int, cursor string) ([]DocumentView, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, title, source_type, status, phase, error, chunk_count
		 FROM document WHERE kb_id=$1 AND id>$2 ORDER BY id ASC LIMIT $3`,
		kbID, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []DocumentView
	for rows.Next() {
		var d DocumentView
		if err := rows.Scan(&d.ID, &d.Title, &d.SourceType, &d.Status, &d.Phase, &d.Error, &d.ChunkCount); err != nil {
			return nil, "", err
		}
		out = append(out, d)
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

// Retry re-enqueues a failed/dead document's job (spec §16.2 POST .../retry):
// resets the job to pending+due-now with attempts cleared, and the document to
// pending. Errors if no job exists for the document.
func (s *Service) Retry(ctx context.Context, kbID, docID string) error {
	// Job-state vocabulary (ingest_job.state): pending / running / done / dead.
	// 'failed' is a document.status, NOT a job state; the worker's fail() only
	// produces 'dead' (terminal) or 'pending' (backoff retry). Only a 'dead' job
	// is manually retryable — a 'done' job already indexed successfully.
	tag, err := s.pool.Exec(ctx,
		`UPDATE ingest_job j SET state='pending', attempts=0, next_run_at=now(), locked_by='', locked_until=NULL, last_error=''
		 FROM document d
		 WHERE j.document_id=d.id AND d.id=$1 AND d.kb_id=$2 AND j.state = 'dead'`,
		docID, kbID)
	if err != nil {
		return fmt.Errorf("ingest: retry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ingest: no retryable job for document %s", docID)
	}
	_, err = s.pool.Exec(ctx, `UPDATE document SET status='pending', error='', phase='' WHERE id=$1`, docID)
	return err
}

func newID() string {
	b := make([]byte, 16)
	_, _ = randRead(b)
	return hex.EncodeToString(b)
}

func randRead(b []byte) (int, error) { return rand.Read(b) }
