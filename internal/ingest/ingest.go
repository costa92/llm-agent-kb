// Package ingest parses uploaded/pasted text into a ragingest.Document and
// imports it synchronously via the RagPort (spec §6, M1 = sync, MD/TXT/paste).
package ingest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ragingest "github.com/costa92/llm-agent-rag/ingest"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

func checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
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

// Result is the outcome of Ingest.
type Result struct {
	DocumentID string
	Status     string
	ChunkCount int
}

// Ingest writes the document row, parses, imports synchronously, and marks the
// document ready. On parse/import failure the row is marked failed with the
// error recorded.
func (s *Service) Ingest(ctx context.Context, in IngestInput) (Result, error) {
	deps := parseDeps{parseTimeout: 30 * time.Second}
	content, _, err := parseSource(ctx, deps, in.SourceType, in.Raw, "")
	if err != nil {
		return Result{}, err
	}
	docID := newID()
	cs := checksum(content)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO document (id, kb_id, title, source_type, source_id, checksum, status)
		 VALUES ($1, $2, $3, $4, $1, $5, 'parsing')`,
		docID, in.KBID, in.Title, string(in.SourceType), cs); err != nil {
		return Result{}, fmt.Errorf("ingest: insert document: %w", err)
	}

	doc := makeDocument(docID, in.KBID, in.SourceType, in.Title, content)
	res, err := s.rag.Import(ctx, []ragingest.Document{doc}, ragingest.ImportOptions{
		Namespace:     in.Namespace,
		ReplaceSource: true,
	})
	if err != nil {
		_, _ = s.pool.Exec(ctx,
			`UPDATE document SET status='failed', error=$2 WHERE id=$1`, docID, err.Error())
		return Result{}, fmt.Errorf("ingest: import: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE document SET status='ready', chunk_count=$2 WHERE id=$1`, docID, res.Chunks); err != nil {
		return Result{}, fmt.Errorf("ingest: mark ready: %w", err)
	}
	return Result{DocumentID: docID, Status: "ready", ChunkCount: res.Chunks}, nil
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
		newID(), docID, docID); err != nil {
		return "", fmt.Errorf("ingest: insert job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return docID, nil
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
