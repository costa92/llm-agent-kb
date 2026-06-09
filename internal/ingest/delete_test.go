package ingest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-contract/llm"
	ragpostgres "github.com/costa92/llm-agent-rag/postgres"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

const liveEnvVar = "LLM_AGENT_KB_PG_URL"

func openIngest(t *testing.T, ctx context.Context) (*Service, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(liveEnvVar)
	if dsn == "" {
		t.Skipf("set %s (pgvector) to run live tests", liveEnvVar)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, tbl := range []string{"document", "knowledge_base", "chunks", "chunks_entities", "chunks_relations", "chunks_communities", "chunks_community_reports"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	chunkStore, err := ragpostgres.New(pool, ragpostgres.Config{Dimension: 8})
	if err != nil {
		t.Fatalf("rag store: %v", err)
	}
	if err := chunkStore.Migrate(ctx); err != nil {
		t.Fatalf("rag migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE knowledge_base (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL, namespace TEXT NOT NULL UNIQUE, embedding_model TEXT NOT NULL DEFAULT '', embedding_dim INT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create kb table: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE document (id TEXT PRIMARY KEY, kb_id TEXT NOT NULL REFERENCES knowledge_base(id) ON DELETE CASCADE, title TEXT NOT NULL, source_type TEXT NOT NULL, source_ref TEXT NOT NULL DEFAULT '', source_id TEXT NOT NULL, checksum TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '', chunk_count INT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create document table: %v", err)
	}
	model := llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "ok"}))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	rag := ragsvc.New(ragsvc.Deps{Model: model, Embedder: embedder, RagStore: chunkStore, ChunkStore: chunkStore})
	return New(pool, rag), pool
}

func TestDeleteDocumentCascade(t *testing.T) {
	ctx := context.Background()
	svc, pool := openIngest(t, ctx)
	_, _ = pool.Exec(ctx, `INSERT INTO knowledge_base (id, org_id, name, namespace, embedding_dim) VALUES ('kb1','org1','KB','ns1',8)`)
	res, err := svc.Ingest(ctx, IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypeMarkdown, Raw: []byte("# H\n\nbody one body two body three")})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.ChunkCount == 0 {
		t.Fatal("expected at least one chunk")
	}
	// Chunks exist before delete.
	ids, _ := svc.rag.ListChunkIDs(ctx, "ns1", res.DocumentID)
	if len(ids) == 0 {
		t.Fatal("expected chunks before delete")
	}
	if err := svc.DeleteDocument(ctx, "ns1", res.DocumentID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	// Chunks gone.
	ids, _ = svc.rag.ListChunkIDs(ctx, "ns1", res.DocumentID)
	if len(ids) != 0 {
		t.Fatalf("chunks remain after delete: %d", len(ids))
	}
	// document row gone.
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM document WHERE id=$1`, res.DocumentID).Scan(&n)
	if n != 0 {
		t.Fatalf("document row remains: %d", n)
	}
}
