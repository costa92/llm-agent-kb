// Package storage owns the pgxpool, the kb business-table migrations
// (knowledge_base + document in M1), and the rag postgres.Store.
package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ragpostgres "github.com/costa92/llm-agent-rag/postgres"
)

// Config configures Open.
type Config struct {
	PGURL        string
	EmbeddingDim int // chunks vector(dim); must match the embedding model
}

// Storage holds the pool and the rag store.
type Storage struct {
	pool     *pgxpool.Pool
	ragStore *ragpostgres.Store
}

// Open builds the pool (registering the pgvector codec on every connection)
// and the rag postgres.Store. The caller owns Close.
func Open(ctx context.Context, cfg Config) (*Storage, error) {
	if cfg.PGURL == "" {
		return nil, fmt.Errorf("storage: PGURL is required")
	}
	if cfg.EmbeddingDim <= 0 {
		return nil, fmt.Errorf("storage: EmbeddingDim must be > 0")
	}
	// Bootstrap the pgvector extension on a plain connection (no codec
	// registration) so the typed pool's AfterConnect=RegisterTypes can find
	// the vector type on every connection during cold-start.
	if err := ensureVectorExtension(ctx, cfg.PGURL); err != nil {
		return nil, err
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.PGURL)
	if err != nil {
		return nil, fmt.Errorf("storage: parse dsn: %w", err)
	}
	// Register the pgvector type codec on every pooled connection.
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return ragpostgres.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("storage: new pool: %w", err)
	}
	ragStore, err := ragpostgres.New(pool, ragpostgres.Config{
		Dimension: cfg.EmbeddingDim,
		// VectorIndex left at default (none) for M1; add IVFFlat/HNSW later.
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("storage: rag store: %w", err)
	}
	return &Storage{pool: pool, ragStore: ragStore}, nil
}

// ensureVectorExtension creates the pgvector extension on a plain connection
// that does NOT register the vector codec, so the typed pool (whose
// AfterConnect registers types) can find the vector type on cold-start.
func ensureVectorExtension(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("storage: bootstrap connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("storage: create vector extension: %w", err)
	}
	return nil
}

// Pool returns the underlying pgxpool.
func (s *Storage) Pool() *pgxpool.Pool { return s.pool }

// RagStore returns the rag postgres.Store (used by ragsvc for vector ops + delete).
func (s *Storage) RagStore() *ragpostgres.Store { return s.ragStore }

// Close releases the pool.
func (s *Storage) Close() { s.pool.Close() }

// businessMigrations are the M1 kb-owned tables. ingest_job, qa_session,
// qa_message, and eval_run are deferred to their milestones (spec §13).
var businessMigrations = []string{
	`CREATE TABLE IF NOT EXISTS knowledge_base (
		id              TEXT PRIMARY KEY,
		org_id          TEXT NOT NULL,
		name            TEXT NOT NULL,
		namespace       TEXT NOT NULL UNIQUE,
		embedding_model TEXT NOT NULL DEFAULT '',
		embedding_dim   INT  NOT NULL DEFAULT 0,
		created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS document (
		id           TEXT PRIMARY KEY,
		kb_id        TEXT NOT NULL REFERENCES knowledge_base(id) ON DELETE CASCADE,
		title        TEXT NOT NULL,
		source_type  TEXT NOT NULL,
		source_ref   TEXT NOT NULL DEFAULT '',
		source_id    TEXT NOT NULL,
		checksum     TEXT NOT NULL DEFAULT '',
		status       TEXT NOT NULL DEFAULT 'pending',
		error        TEXT NOT NULL DEFAULT '',
		chunk_count  INT  NOT NULL DEFAULT 0,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS document_kb_idx ON document (kb_id)`,
}

// Migrate applies the rag store migrations (chunks/graph/community + the
// pgvector extension) then the kb business migrations. Idempotent.
func (s *Storage) Migrate(ctx context.Context) error {
	if err := s.ragStore.Migrate(ctx); err != nil {
		return fmt.Errorf("storage: rag migrate: %w", err)
	}
	for _, stmt := range businessMigrations {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("storage: business migrate: %w", err)
		}
	}
	return nil
}
