package ragsvc

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-contract/llm"
	raggenerate "github.com/costa92/llm-agent-rag/generate"
	raggraph "github.com/costa92/llm-agent-rag/graph"
	ragingest "github.com/costa92/llm-agent-rag/ingest"
	ragpostgres "github.com/costa92/llm-agent-rag/postgres"
	ragcore "github.com/costa92/llm-agent-rag/rag"
	ragstore "github.com/costa92/llm-agent-rag/store"
)

// Compile-time: the concrete service must satisfy RagPort.
var _ RagPort = (*Service)(nil)

// Compile-time: the widened RagPort surface. Community reads return kb-local
// DTOs (CommunityView/CommunityReportView) so importers never see rag/graph.
var _ interface {
	AskGlobal(ctx context.Context, question string, req GlobalRequest) (ragcore.Answer, error)
	AskDrift(ctx context.Context, question string, req DriftRequest) (ragcore.Answer, error)
	PrewarmCommunityReports(ctx context.Context, namespace string) (int, error)
	ListCommunities(ctx context.Context, namespace string) ([]CommunityView, error)
	CommunityReport(ctx context.Context, namespace, communityID string) (CommunityReportView, bool, error)
} = (*Service)(nil)

func TestAskGlobalEmptyWhenNoCommunities(t *testing.T) {
	// In-memory store, no detector configured → zero communities → AskGlobal
	// returns an empty (no-error) answer. Proves the Inner() delegation + span
	// path compiles and runs without a DB.
	model := llm.NewScriptedLLM(llm.WithResponses(llm.TextResponse("unused")))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	svc := New(Deps{Model: model, Embedder: embedder})
	ctx := context.Background()
	ans, err := svc.AskGlobal(ctx, "what themes?", GlobalRequest{Namespace: "ns", MaxCommunities: 4})
	if err != nil {
		t.Fatalf("AskGlobal: %v", err)
	}
	if ans.Text != "" {
		t.Fatalf("AskGlobal over a community-less namespace = %q, want empty", ans.Text)
	}
}

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

// TestGraphComponentsWiredImport proves New passes the graph components into
// rag.Options so Import drives the auto-extract/auto-detect path without error.
// Uses the DETERMINISTIC DictionaryEntityExtractor + LouvainDetector + a fixed
// summarizer — NO scripted-LLM cursor counting (rag's own community_test.go
// pattern). The in-memory rag store implements CommunityStore, so Import's
// detection block runs; community READS (ListCommunities) go through the held
// postgres.Store, so they are asserted in the gated Task 4, not here.
func TestGraphComponentsWiredImport(t *testing.T) {
	model := llm.NewScriptedLLM(llm.WithResponses(llm.TextResponse("unused")))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	svc := New(Deps{
		Model: model, Embedder: embedder,
		EntityExtractor: raggraph.DictionaryEntityExtractor{Terms: map[string]string{
			"alpha": "topic", "bravo": "topic", "carbon": "topic", "delta": "topic",
		}},
		CommunityDetector:   raggraph.LouvainDetector{},
		CommunitySummarizer: fixedSummarizer{},
	})
	ctx := context.Background()
	docs := []ragingest.Document{
		{ID: "d1", SourceID: "d1", Title: "T1", Content: "alpha bravo alpha bravo"},
		{ID: "d2", SourceID: "d2", Title: "T2", Content: "carbon delta carbon delta"},
	}
	// Import must succeed with the graph seams attached (auto-extract +
	// auto-detect run internally against the in-memory store; summarization is
	// lazy so the scripted model's "unused" response is never consumed).
	if _, err := svc.Import(ctx, docs, ragingest.ImportOptions{Namespace: "ns", ReplaceSource: true}); err != nil {
		t.Fatalf("Import with graph components wired: %v", err)
	}
}

// TestGraphRAGLivePgvector ingests two thematically-distinct docs into a real
// pgvector store with the deterministic graph components, asserts communities
// land in the store, prewarms reports, then asserts AskGlobal returns the
// scripted reduce text. Gated on LLM_AGENT_KB_PG_URL. Owns its DB (drops the
// rag tables it uses).
func TestGraphRAGLivePgvector(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the live GraphRAG test")
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
	for _, tbl := range []string{"chunks_community_reports", "chunks_communities", "chunks_relations", "chunks_entities", "chunks"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	chunkStore, err := ragpostgres.New(pool, ragpostgres.Config{Dimension: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := chunkStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	model := llm.NewScriptedLLM(llm.WithResponses(
		// Map responses MUST carry a `Score:` prefix — rag's parseGlobalMap only
		// assigns a point-score on a leading `Score:` line (global.go:157); plain
		// text parses to score 0, the reduce stage drops all score-0 partials,
		// ReduceCalls stays 0, and AskGlobal returns the "no relevant community
		// information" answer (mirrors rag's own global_test.go scripting).
		llm.TextResponse("Score: 80\nmap-a"), llm.TextResponse("Score: 80\nmap-b"),
		llm.TextResponse("Score: 80\nmap-c"), llm.TextResponse("Score: 80\nmap-d"),
		llm.TextResponse("GLOBAL ANSWER: two themes"),
	))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	svc := New(Deps{
		Model: model, Embedder: embedder,
		RagStore: chunkStore, ChunkStore: chunkStore,
		EntityExtractor: raggraph.DictionaryEntityExtractor{Terms: map[string]string{
			"alpha": "topic", "bravo": "topic", "carbon": "topic", "delta": "topic",
		}},
		CommunityDetector:   raggraph.LouvainDetector{},
		CommunitySummarizer: fixedSummarizer{},
	})
	docs := []ragingest.Document{
		{ID: "d1", SourceID: "d1", Title: "T1", Content: "alpha bravo alpha bravo alpha"},
		{ID: "d2", SourceID: "d2", Title: "T2", Content: "carbon delta carbon delta carbon"},
	}
	if _, err := svc.Import(ctx, docs, ragingest.ImportOptions{Namespace: "g1", ReplaceSource: true}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	comms, err := svc.ListCommunities(ctx, "g1")
	if err != nil {
		t.Fatalf("ListCommunities: %v", err)
	}
	if len(comms) == 0 {
		t.Fatal("Import did not persist communities to pgvector")
	}
	if _, err := svc.PrewarmCommunityReports(ctx, "g1"); err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	rep, ok, err := svc.CommunityReport(ctx, "g1", comms[0].ID)
	if err != nil || !ok {
		t.Fatalf("CommunityReport(%s) ok=%v err=%v", comms[0].ID, ok, err)
	}
	if rep.Title == "" {
		t.Fatalf("prewarmed report has empty title")
	}
	ans, err := svc.AskGlobal(ctx, "what are the themes?", GlobalRequest{Namespace: "g1", MaxCommunities: 8})
	if err != nil {
		t.Fatalf("AskGlobal: %v", err)
	}
	if ans.Text == "" {
		t.Fatalf("AskGlobal returned empty text over a populated namespace")
	}
	if ans.Diagnostics.Global.ReduceCalls != 1 {
		t.Fatalf("expected exactly 1 reduce call, got %d (map=%d)", ans.Diagnostics.Global.ReduceCalls, ans.Diagnostics.Global.MapCalls)
	}
}

// fixedSummarizer is a deterministic CommunitySummarizer (mirrors rag's
// staticSummarizer) so reports are reproducible without an LLM cursor. Reused
// by Task 4's gated live-pgvector test.
type fixedSummarizer struct{}

func (fixedSummarizer) Summarize(_ context.Context, c raggraph.Community, _ raggraph.Graph) (raggraph.CommunityReport, error) {
	return raggraph.CommunityReport{
		CommunityID: c.ID,
		Title:       "Theme " + c.ID,
		Summary:     "Summary of community " + c.ID,
		ContentHash: raggraph.CommunityContentHash(c),
	}, nil
}

func TestNewLLMGraphComponentsHonorsResolverToggle(t *testing.T) {
	model := llm.NewScriptedLLM(llm.WithResponses(llm.TextResponse("unused")))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	off := NewLLMGraphComponents(model, embedder, GraphConfig{LouvainResolution: 1.0, ResolverEnabled: false})
	if off.EntityExtractor == nil || off.CommunityDetector == nil || off.CommunitySummarizer == nil {
		t.Fatal("core components must be non-nil")
	}
	if off.EntityResolver != nil {
		t.Fatal("resolver disabled → EntityResolver must be nil (rag defaults to Noop)")
	}
	on := NewLLMGraphComponents(model, embedder, GraphConfig{LouvainResolution: 1.5, ResolverEnabled: true, ResolverThreshold: 0.9})
	if on.EntityResolver == nil {
		t.Fatal("resolver enabled → EntityResolver must be set")
	}
}

func TestServiceExposesJudgeModel(t *testing.T) {
	svc := New(Deps{
		Model:    llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: `{"groundedness":1,"answer_relevance":1}`})),
		Embedder: llm.NewScriptedLLM(llm.WithEmbedDimensions(4)),
	})
	jm := svc.JudgeModel()
	if jm == nil {
		t.Fatal("JudgeModel() returned nil")
	}
	resp, err := jm.Generate(context.Background(), raggenerate.Request{
		Messages: []raggenerate.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("judge model generate: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("judge model returned empty text")
	}
}
