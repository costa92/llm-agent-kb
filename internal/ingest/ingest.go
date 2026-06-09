// Package ingest parses uploaded/pasted text into a ragingest.Document and
// imports it synchronously via the RagPort (spec §6, M1 = sync, MD/TXT/paste).
package ingest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	ragingest "github.com/costa92/llm-agent-rag/ingest"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

// SourceType is the M1 set of accepted document source types. PDF/DOCX/URL are M2.
type SourceType string

const (
	SourceTypeMarkdown SourceType = "markdown"
	SourceTypeTXT      SourceType = "txt"
	SourceTypePaste    SourceType = "paste"
)

// parse converts raw bytes to text for an M1 source type. MD/TXT/paste are all
// treated as text (the rag splitter handles markdown structure downstream).
func parse(st SourceType, raw []byte) (string, error) {
	switch st {
	case SourceTypeMarkdown, SourceTypeTXT, SourceTypePaste:
		return string(raw), nil
	default:
		return "", fmt.Errorf("ingest: unsupported source_type %q (M1 supports markdown/txt/paste)", st)
	}
}

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
	content, err := parse(in.SourceType, in.Raw)
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

func newID() string {
	b := make([]byte, 16)
	_, _ = randRead(b)
	return hex.EncodeToString(b)
}

func randRead(b []byte) (int, error) { return rand.Read(b) }
