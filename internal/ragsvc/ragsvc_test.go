package ragsvc

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-contract/llm"
	ragingest "github.com/costa92/llm-agent-rag/ingest"
	ragpostgres "github.com/costa92/llm-agent-rag/postgres"
	ragstore "github.com/costa92/llm-agent-rag/store"
)

// Compile-time: the concrete service must satisfy RagPort.
var _ RagPort = (*Service)(nil)

func TestNewWiresInMemorySystemForUnitTest(t *testing.T) {
	// With a nil store the rag default in-memory store is used; this proves
	// New wires the adapters + system without a DB. (Delete ops need the
	// postgres store and are exercised in the storage-gated cascade test.)
	model := llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "hello", Usage: llm.Usage{TotalTokens: 3}}))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	svc := New(Deps{Model: model, Embedder: embedder, RagStore: nil, ChunkStore: nil})
	if svc == nil {
		t.Fatal("New returned nil")
	}
	ctx := context.Background()
	if _, err := svc.Import(ctx, []ragingest.Document{{
		ID: "d1", SourceID: "d1", Content: "hello world", Title: "T",
	}}, ragingest.ImportOptions{Namespace: "ns"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	ans, err := svc.Ask(ctx, "hi", AskRequest{Namespace: "ns", TopK: 3, Hybrid: false})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Text != "hello" {
		t.Fatalf("answer text=%q want hello", ans.Text)
	}
	// store deref guard: ChunkStore nil → delete ops error, not panic.
	if _, err := svc.ListChunkIDs(ctx, "ns", "d1"); err == nil {
		t.Fatal("ListChunkIDs with nil ChunkStore should error")
	}
	_ = ragstore.Filter{}
}

// TestAskReturnsHitsOnLivePgvector is the M2 regression guard: it imports a doc
// into a real pgvector store then asserts the Ask path returns at least one
// hit/citation. If SecurityFilters were (wrongly) set to {"namespace":...}, the
// `metadata @> $n` filter would match zero chunks and this test would catch it.
// Gated on LLM_AGENT_KB_PG_URL (pgvector-enabled).
func TestAskReturnsHitsOnLivePgvector(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the live Ask test")
	}
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return ragpostgres.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, tbl := range []string{"chunks", "chunks_entities", "chunks_relations", "chunks_communities", "chunks_community_reports"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	chunkStore, err := ragpostgres.New(pool, ragpostgres.Config{Dimension: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := chunkStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	model := llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "answer", Usage: llm.Usage{TotalTokens: 3}}))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	svc := New(Deps{Model: model, Embedder: embedder, RagStore: chunkStore, ChunkStore: chunkStore})

	if _, err := svc.Import(ctx, []ragingest.Document{{
		ID: "d1", SourceID: "d1", Title: "T",
		Content: "the quick brown fox jumps over the lazy dog repeatedly",
	}}, ragingest.ImportOptions{Namespace: "ns1", ReplaceSource: true}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	ans, err := svc.Ask(ctx, "fox", AskRequest{Namespace: "ns1", TopK: 5})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Diagnostics.HitCount == 0 && len(ans.Citations) == 0 {
		t.Fatalf("Ask returned no hits/citations (HitCount=%d, citations=%d) — Namespace isolation broken or SecurityFilters regression", ans.Diagnostics.HitCount, len(ans.Citations))
	}
}
