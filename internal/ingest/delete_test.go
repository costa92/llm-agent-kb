package ingest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-contract/llm"
	raggraph "github.com/costa92/llm-agent-rag/graph"
	ragingest "github.com/costa92/llm-agent-rag/ingest"
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

// openIngestGraph builds a graph-enabled gated ingest.Service + ragsvc.Service
// over a fresh DB with the DETERMINISTIC graph components (mirrors the ragsvc
// gated test), so Import auto-detects communities and DeleteDocument can drive
// the §16.4 recompute. RegisterTypes is wired so GraphSnapshot reads the entity
// graph correctly.
func openIngestGraph(t *testing.T, ctx context.Context) (*Service, *pgxpool.Pool, *ragsvc.Service) {
	t.Helper()
	dsn := os.Getenv(liveEnvVar)
	if dsn == "" {
		t.Skipf("set %s (pgvector) to run live tests", liveEnvVar)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return ragpostgres.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, tbl := range []string{"ingest_job", "document", "knowledge_base", "chunks_community_reports", "chunks_communities", "chunks_relations", "chunks_entities", "chunks"} {
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
	model := llm.NewScriptedLLM(llm.WithResponses(llm.TextResponse("unused")))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	rag := ragsvc.New(ragsvc.Deps{
		Model: model, Embedder: embedder,
		RagStore: chunkStore, ChunkStore: chunkStore,
		EntityExtractor: raggraph.DictionaryEntityExtractor{Terms: map[string]string{
			"alpha": "topic", "bravo": "topic", "carbon": "topic", "delta": "topic",
		}},
		CommunityDetector:   raggraph.LouvainDetector{},
		CommunitySummarizer: deleteFixedSummarizer{},
	})
	return New(pool, rag), pool, rag
}

func TestDeleteRecomputesCommunities(t *testing.T) {
	ctx := context.Background()
	s, pool, rag := openIngestGraph(t, ctx)
	// kb namespace + document rows so the §16.4 row-delete step has rows to drop.
	if _, err := pool.Exec(ctx, `INSERT INTO knowledge_base (id, org_id, name, namespace, embedding_dim) VALUES ('kbd','org1','KB','kb_del',8)`); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	for _, id := range []string{"d1", "d2"} {
		if _, err := pool.Exec(ctx, `INSERT INTO document (id, kb_id, title, source_type, source_id) VALUES ($1,'kbd',$1,'markdown',$1)`, id); err != nil {
			t.Fatalf("insert document %s: %v", id, err)
		}
	}
	// Import two thematically-distinct docs directly so communities are detected.
	docs := []ragingest.Document{
		{ID: "d1", SourceID: "d1", Title: "T1", Content: "alpha bravo alpha bravo alpha"},
		{ID: "d2", SourceID: "d2", Title: "T2", Content: "carbon delta carbon delta carbon"},
	}
	if _, err := rag.Import(ctx, docs, ragingest.ImportOptions{Namespace: "kb_del", ReplaceSource: true}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	before, _ := rag.ListCommunities(ctx, "kb_del")
	if len(before) == 0 {
		t.Fatal("setup: expected communities before delete")
	}

	if err := s.DeleteDocument(ctx, "kb_del", "d1"); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	after, err := rag.ListCommunities(ctx, "kb_del")
	if err != nil {
		t.Fatalf("ListCommunities: %v", err)
	}
	// d1's chunks/graph are gone; the recompute re-ran Louvain over the remaining
	// (d2-only) graph and replaced the namespace community set.
	if len(after) == 0 {
		t.Fatal("delete left no communities — recompute did not run over the remaining graph")
	}
	// The surviving community's report must have been prewarmed by the recompute.
	if _, ok, _ := rag.CommunityReport(ctx, "kb_del", after[0].ID); !ok {
		t.Fatal("recompute did not prewarm the surviving community's report")
	}

	// Empty-namespace edge (§16.4): deleting the LAST document leaves the
	// namespace graph empty → GraphSnapshot empty → Detect zero communities →
	// UpsertCommunities replace-all deletes the prior set → Prewarm returns
	// (0, nil). RecomputeCommunities must return no error and leave zero
	// communities (the recompute must not choke on an empty graph).
	if err := s.DeleteDocument(ctx, "kb_del", "d2"); err != nil {
		t.Fatalf("DeleteDocument(last doc): %v", err)
	}
	empty, err := rag.ListCommunities(ctx, "kb_del")
	if err != nil {
		t.Fatalf("ListCommunities after last delete: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("deleting the last document left %d communities, want 0", len(empty))
	}
}

// deleteFixedSummarizer is a deterministic CommunitySummarizer so prewarmed
// reports are reproducible without an LLM cursor.
type deleteFixedSummarizer struct{}

func (deleteFixedSummarizer) Summarize(_ context.Context, c raggraph.Community, _ raggraph.Graph) (raggraph.CommunityReport, error) {
	return raggraph.CommunityReport{
		CommunityID: c.ID,
		Title:       "Theme " + c.ID,
		Summary:     "Summary of community " + c.ID,
		ContentHash: raggraph.CommunityContentHash(c),
	}, nil
}
