package ingest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-contract/llm"
	ragpostgres "github.com/costa92/llm-agent-rag/postgres"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

const liveEnvVar = "LLM_AGENT_KB_PG_URL"

func openIngest(t *testing.T, ctx context.Context) (*Service, *pgxpool.Pool, *ragsvc.Service) {
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
	for _, tbl := range []string{"ingest_job", "document", "knowledge_base", "chunks", "chunks_entities", "chunks_relations", "chunks_communities", "chunks_community_reports"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	chunkStore, err := ragpostgres.New(pool, ragpostgres.Config{Dimension: 8})
	if err != nil {
		t.Fatalf("rag store: %v", err)
	}
	if err := chunkStore.Migrate(ctx); err != nil {
		t.Fatalf("rag migrate: %v", err)
	}
	for _, stmt := range businessMigrationsForTest {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("migrate %q: %v", stmt, err)
		}
	}
	model := llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "ok"}))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	rag := ragsvc.New(ragsvc.Deps{Model: model, Embedder: embedder, RagStore: chunkStore, ChunkStore: chunkStore})
	return New(pool, rag), pool, rag
}

func TestDeleteDocumentCascade(t *testing.T) {
	ctx := context.Background()
	svc, pool, rag := openIngest(t, ctx)
	_, _ = pool.Exec(ctx, `INSERT INTO knowledge_base (id, org_id, name, namespace, embedding_dim) VALUES ('kb1','org1','KB','ns1',8)`)
	// Seed a ready document with chunks via the async path: Enqueue then drain the
	// queue with a Worker (the M1 sync Ingest was removed in Task 9).
	docID, err := svc.Enqueue(ctx, IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypeMarkdown, Raw: []byte("# H\n\nbody one body two body three")})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w := NewWorker(WorkerConfig{Pool: pool, Rag: rag, WorkerID: "w1", Lease: time.Minute, MaxAttempts: 5, BaseBackoff: time.Second})
	if claimed, err := w.RunOnce(ctx); err != nil || !claimed {
		t.Fatalf("RunOnce claimed=%v err=%v", claimed, err)
	}
	var cc int
	if err := pool.QueryRow(ctx, `SELECT chunk_count FROM document WHERE id=$1`, docID).Scan(&cc); err != nil || cc == 0 {
		t.Fatalf("chunk_count=%d err=%v want >0", cc, err)
	}
	// Chunks exist before delete.
	ids, _ := svc.rag.ListChunkIDs(ctx, "ns1", docID)
	if len(ids) == 0 {
		t.Fatal("expected chunks before delete")
	}
	if err := svc.DeleteDocument(ctx, "ns1", docID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	// Chunks gone.
	ids, _ = svc.rag.ListChunkIDs(ctx, "ns1", docID)
	if len(ids) != 0 {
		t.Fatalf("chunks remain after delete: %d", len(ids))
	}
	// document row gone.
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM document WHERE id=$1`, docID).Scan(&n)
	if n != 0 {
		t.Fatalf("document row remains: %d", n)
	}
}
