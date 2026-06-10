package ragsvc

import (
	"context"
	"fmt"

	"github.com/costa92/llm-agent-contract/llm"
	"github.com/costa92/llm-agent-otel/otelrag"
	raggenerate "github.com/costa92/llm-agent-rag/generate"
	raggraph "github.com/costa92/llm-agent-rag/graph"
	ragingest "github.com/costa92/llm-agent-rag/ingest"
	ragpostgres "github.com/costa92/llm-agent-rag/postgres"
	ragcore "github.com/costa92/llm-agent-rag/rag"
	ragstore "github.com/costa92/llm-agent-rag/store"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// AskRequest is the kb-side ask request. Hybrid=false is vector mode
// (lexical/rerank off); Hybrid=true enables reranking. NO global/drift in M1.
type AskRequest struct {
	Namespace      string
	TopK           int
	Hybrid         bool
	MaxTotalTokens int
}

// GlobalRequest is the kb-side AskGlobal request. Isolation is Namespace-ONLY
// (spec §8): GlobalOptions has no SecurityFilters; namespace isolation runs
// after RBAC.
type GlobalRequest struct {
	Namespace      string
	MaxCommunities int
	MaxTotalTokens int
}

// DriftRequest is the kb-side AskDrift request. Namespace-only isolation (§8).
type DriftRequest struct {
	Namespace      string
	MaxCommunities int
	Rounds         int
	TopK           int
	MaxTotalTokens int
}

// RagPort is the narrow rag surface every non-rag kb package depends on.
// M1: Ask + Import + the three delete primitives (§16.4).
type RagPort interface {
	Ask(ctx context.Context, question string, req AskRequest) (ragcore.Answer, error)
	// Retrieve runs retrieval only (no generation) — backs the kb eval
	// RetrievalEvaluator (§9). Delegated to the otelrag Wrapper (auto-span).
	Retrieve(ctx context.Context, query string, opts ragcore.SearchOptions) ([]ragstore.Hit, error)
	// StreamAnswer runs the M5a single-pass grounded streaming answer
	// (Option A): retrieve → render rag's default QA prompt → model.Stream,
	// delivering token deltas + a terminal done event to emit. Bypasses rag's
	// reflection/grader orchestration by design (the non-stream Ask keeps it).
	StreamAnswer(ctx context.Context, question string, req StreamRequest, emit func(StreamEvent) error) error
	Import(ctx context.Context, docs []ragingest.Document, opts ragingest.ImportOptions) (ragingest.ImportResult, error)
	// ListChunkIDs returns the chunk IDs for a source in a namespace
	// (collected BEFORE removal so the graph can be reconciled by ID).
	ListChunkIDs(ctx context.Context, namespace, sourceID string) ([]string, error)
	// RemoveGraphBySource drops the given chunk IDs from the entity graph.
	RemoveGraphBySource(ctx context.Context, namespace string, chunkIDs []string) error
	// RemoveChunks deletes every chunk for a source in a namespace.
	RemoveChunks(ctx context.Context, namespace, sourceID string) (int, error)
	// M3 GraphRAG. AskGlobal/AskDrift/PrewarmCommunityReports delegate to
	// Wrapper.Inner() (*rag.System) with kb-self-instrumented spans — the
	// otelrag Wrapper does NOT instrument the GraphRAG paths (§11).
	AskGlobal(ctx context.Context, question string, req GlobalRequest) (ragcore.Answer, error)
	AskDrift(ctx context.Context, question string, req DriftRequest) (ragcore.Answer, error)
	PrewarmCommunityReports(ctx context.Context, namespace string) (int, error)
	// RecomputeCommunities re-detects the namespace's communities over the
	// CURRENT (post-delete) graph and refreshes reports. §16.4: communities
	// cannot be deleted per-source (Louvain is full-namespace), so after a
	// delete the caller recomputes. No-op when the store/detector is absent.
	RecomputeCommunities(ctx context.Context, namespace string) error
	// Community views read directly from the held postgres.Store
	// (a store.CommunityStore) and return kb-local DTOs (not rag/graph types)
	// so importers (retrieval/httpapi) never depend on rag/graph (spec §4).
	ListCommunities(ctx context.Context, namespace string) ([]CommunityView, error)
	CommunityReport(ctx context.Context, namespace, communityID string) (CommunityReportView, bool, error)
}

// CommunityView is the kb-local projection of a graph.Community. Keeping it
// here (not exposing raggraph.Community) makes ragsvc the SOLE importer of
// rag/graph (spec §4) — retrieval/httpapi/cmd-kbd type against these DTOs.
type CommunityView struct {
	ID          string
	Level       int
	ParentID    string
	EntityCount int
}

// CommunityReportView is the kb-local projection of a graph.CommunityReport.
type CommunityReportView struct {
	ID      string // the CommunityID the report describes
	Title   string
	Summary string
}

// Deps are the construction inputs for the service.
type Deps struct {
	Model      llm.ChatModel
	Embedder   llm.Embedder
	RagStore   ragstore.Store       // backing store for rag.New (the postgres.Store)
	ChunkStore *ragpostgres.Store   // same store, concrete, for List/Remove delete ops
	Tracer     trace.TracerProvider // optional; nil → no-op spans

	// M3 GraphRAG seams (spec §6 step 4). Nil leaves the path disabled —
	// Import skips extraction/detection gracefully (rag/import.go:201). Tests
	// inject deterministic substitutes; production injects LLM-backed ones.
	EntityExtractor     raggraph.EntityExtractor
	EntityResolver      raggraph.EntityResolver // optional near-dup merge; nil → rag NoopEntityResolver
	CommunityDetector   raggraph.CommunityDetector
	CommunitySummarizer raggraph.CommunitySummarizer
}

// Service is the only unit that holds the rag backend.
type Service struct {
	wrapper    *otelrag.Wrapper
	chunkStore *ragpostgres.Store
	tracer     trace.Tracer
	detector   raggraph.CommunityDetector // for §16.4 post-delete recompute; nil → recompute is a no-op
	model      llm.ChatModel              // kept for the eval LLM-as-judge seam (JudgeModel)
}

// New wires the adapters, rag.System, and the otelrag wrapper.
func New(d Deps) *Service {
	sys := ragcore.New(ragcore.Options{
		Model:               ragModelAdapter{inner: d.Model},
		Embedder:            ragEmbedderAdapter{inner: d.Embedder},
		Store:               d.RagStore, // nil → rag default in-memory store (unit tests)
		EntityExtractor:     d.EntityExtractor,
		EntityResolver:      d.EntityResolver, // nil → rag defaults to NoopEntityResolver
		CommunityDetector:   d.CommunityDetector,
		CommunitySummarizer: d.CommunitySummarizer,
	})
	var wrapper *otelrag.Wrapper
	if d.Tracer != nil {
		wrapper = otelrag.Wrap(sys, otelrag.Config{TracerProvider: d.Tracer})
	} else {
		wrapper = otelrag.Wrap(sys)
	}
	// The otelrag Wrapper holds the same TracerProvider but exposes no
	// GraphRAG spans, so kb keeps its own tracer for AskGlobal/AskDrift/Prewarm.
	var tracer trace.Tracer
	if d.Tracer != nil {
		tracer = d.Tracer.Tracer("github.com/costa92/llm-agent-kb/internal/ragsvc")
	} else {
		tracer = noop.NewTracerProvider().Tracer("ragsvc")
	}
	return &Service{wrapper: wrapper, chunkStore: d.ChunkStore, tracer: tracer, detector: d.CommunityDetector, model: d.Model}
}

func (s *Service) Ask(ctx context.Context, question string, req AskRequest) (ragcore.Answer, error) {
	// M1 tenant isolation is by Namespace ALONE (one kb per namespace). We do
	// NOT set SecurityFilters: it is applied as `metadata @> $n` (postgres.go
	// buildWhere → metadataJSONFilter), and chunks carry no "namespace"
	// metadata key, so {"namespace":...} would match zero chunks and silently
	// return no hits. Per-source/per-field metadata filtering is a later
	// concern (multi-source-per-namespace), out of M1 scope.
	return s.wrapper.Ask(ctx, question, ragcore.AskOptions{
		Search: ragcore.SearchOptions{
			Namespace:    req.Namespace,
			TopK:         req.TopK,
			EnableRerank: req.Hybrid, // hybrid on → rerank on; vector mode → off
		},
		MaxTotalTokens: req.MaxTotalTokens,
	})
}

func (s *Service) Import(ctx context.Context, docs []ragingest.Document, opts ragingest.ImportOptions) (ragingest.ImportResult, error) {
	return s.wrapper.Import(ctx, docs, opts)
}

// Retrieve runs retrieval only via the otelrag Wrapper (auto-instrumented).
func (s *Service) Retrieve(ctx context.Context, query string, opts ragcore.SearchOptions) ([]ragstore.Hit, error) {
	return s.wrapper.Retrieve(ctx, query, opts)
}

// JudgeModel returns the rag generate.Model seam (the chat model wrapped by
// ragModelAdapter) for the kb eval LLM-as-judge. Kept here so internal/eval
// never constructs a second rag.System or its own adapter (spec §4).
func (s *Service) JudgeModel() raggenerate.Model {
	return ragModelAdapter{inner: s.model}
}

func (s *Service) ListChunkIDs(ctx context.Context, namespace, sourceID string) ([]string, error) {
	if s.chunkStore == nil {
		return nil, fmt.Errorf("ragsvc: chunk store not configured")
	}
	chunks, err := s.chunkStore.List(ctx, namespace, ragstore.Filter{
		ragingest.MetadataSourceIDKey: sourceID,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("ragsvc: list source %s: %w", sourceID, err)
	}
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

func (s *Service) RemoveGraphBySource(ctx context.Context, namespace string, chunkIDs []string) error {
	if s.chunkStore == nil {
		return fmt.Errorf("ragsvc: chunk store not configured")
	}
	return s.chunkStore.RemoveGraphBySource(ctx, namespace, chunkIDs)
}

func (s *Service) RemoveChunks(ctx context.Context, namespace, sourceID string) (int, error) {
	if s.chunkStore == nil {
		return 0, fmt.Errorf("ragsvc: chunk store not configured")
	}
	return s.chunkStore.RemoveByFilter(ctx, namespace, ragstore.Filter{
		ragingest.MetadataSourceIDKey: sourceID,
	})
}

func (s *Service) AskGlobal(ctx context.Context, question string, req GlobalRequest) (ragcore.Answer, error) {
	ctx, span := s.tracer.Start(ctx, "ragsvc.AskGlobal")
	defer span.End()
	span.SetAttributes(attribute.String("rag.namespace", req.Namespace))
	ans, err := s.wrapper.Inner().AskGlobal(ctx, question, ragcore.GlobalOptions{
		Namespace:      req.Namespace,
		MaxCommunities: req.MaxCommunities,
		MaxTotalTokens: req.MaxTotalTokens,
	})
	if err != nil {
		span.RecordError(err)
	}
	return ans, err
}

func (s *Service) AskDrift(ctx context.Context, question string, req DriftRequest) (ragcore.Answer, error) {
	ctx, span := s.tracer.Start(ctx, "ragsvc.AskDrift")
	defer span.End()
	span.SetAttributes(attribute.String("rag.namespace", req.Namespace))
	ans, err := s.wrapper.Inner().AskDrift(ctx, question, ragcore.DriftOptions{
		Namespace:      req.Namespace,
		MaxCommunities: req.MaxCommunities,
		Rounds:         req.Rounds,
		TopK:           req.TopK,
		MaxTotalTokens: req.MaxTotalTokens,
	})
	if err != nil {
		span.RecordError(err)
	}
	return ans, err
}

func (s *Service) PrewarmCommunityReports(ctx context.Context, namespace string) (int, error) {
	ctx, span := s.tracer.Start(ctx, "ragsvc.PrewarmCommunityReports")
	defer span.End()
	span.SetAttributes(attribute.String("rag.namespace", namespace))
	n, err := s.wrapper.Inner().PrewarmCommunityReports(ctx, namespace)
	if err != nil {
		span.RecordError(err)
	}
	return n, err
}

func (s *Service) RecomputeCommunities(ctx context.Context, namespace string) error {
	ctx, span := s.tracer.Start(ctx, "ragsvc.RecomputeCommunities")
	defer span.End()
	span.SetAttributes(attribute.String("rag.namespace", namespace))
	if s.chunkStore == nil || s.detector == nil {
		return nil // graceful: no graph capability → nothing to recompute
	}
	snap, err := s.chunkStore.GraphSnapshot(ctx, namespace)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("ragsvc: recompute snapshot: %w", err)
	}
	communities, err := s.detector.Detect(ctx, snap)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("ragsvc: recompute detect: %w", err)
	}
	if err := s.chunkStore.UpsertCommunities(ctx, namespace, communities); err != nil {
		span.RecordError(err)
		return fmt.Errorf("ragsvc: recompute upsert: %w", err)
	}
	// Refresh reports for the new community set (stale-hash entries regenerate).
	if _, err := s.wrapper.Inner().PrewarmCommunityReports(ctx, namespace); err != nil {
		span.RecordError(err)
		return fmt.Errorf("ragsvc: recompute prewarm: %w", err)
	}
	return nil
}

func (s *Service) ListCommunities(ctx context.Context, namespace string) ([]CommunityView, error) {
	if s.chunkStore == nil {
		return nil, fmt.Errorf("ragsvc: chunk store not configured")
	}
	comms, err := s.chunkStore.Communities(ctx, namespace)
	if err != nil {
		return nil, err
	}
	views := make([]CommunityView, 0, len(comms))
	for _, c := range comms {
		views = append(views, CommunityView{
			ID:          c.ID,
			Level:       c.Level,
			ParentID:    c.ParentID,
			EntityCount: len(c.EntityIDs),
		})
	}
	return views, nil
}

func (s *Service) CommunityReport(ctx context.Context, namespace, communityID string) (CommunityReportView, bool, error) {
	if s.chunkStore == nil {
		return CommunityReportView{}, false, fmt.Errorf("ragsvc: chunk store not configured")
	}
	rep, ok, err := s.chunkStore.CommunityReport(ctx, namespace, communityID)
	if err != nil || !ok {
		return CommunityReportView{}, ok, err
	}
	return CommunityReportView{ID: rep.CommunityID, Title: rep.Title, Summary: rep.Summary}, true, nil
}

// GraphConfig selects the production GraphRAG component parameters.
type GraphConfig struct {
	LouvainResolution float64
	ResolverEnabled   bool
	ResolverThreshold float64
}

// GraphComponents bundles the rag/graph seams cmd/kbd passes into Deps. Keeping
// the raggraph types behind this constructor means cmd/kbd never imports
// rag/graph directly (boundary: ragsvc is the sole importer of rag/*).
type GraphComponents struct {
	EntityExtractor     raggraph.EntityExtractor
	EntityResolver      raggraph.EntityResolver
	CommunityDetector   raggraph.CommunityDetector
	CommunitySummarizer raggraph.CommunitySummarizer
}

// NewLLMGraphComponents builds the production GraphRAG components over the given
// chat model + embedder, honoring the resolver toggle. cmd/kbd calls this so it
// never imports rag/graph directly. A disabled resolver leaves EntityResolver
// nil — rag then defaults to its NoopEntityResolver.
func NewLLMGraphComponents(model llm.ChatModel, embedder llm.Embedder, opts GraphConfig) GraphComponents {
	gc := GraphComponents{
		EntityExtractor:     raggraph.LLMEntityExtractor{Model: ragModelAdapter{inner: model}},
		CommunityDetector:   raggraph.LouvainDetector{Resolution: opts.LouvainResolution},
		CommunitySummarizer: raggraph.LLMCommunitySummarizer{Model: ragModelAdapter{inner: model}},
	}
	if opts.ResolverEnabled {
		gc.EntityResolver = raggraph.EmbeddingEntityResolver{
			Embedder:  ragEmbedderAdapter{inner: embedder},
			Threshold: opts.ResolverThreshold,
		}
	}
	return gc
}
