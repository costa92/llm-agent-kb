package ragsvc

import (
	"context"
	"fmt"

	"github.com/costa92/llm-agent-contract/llm"
	"github.com/costa92/llm-agent-otel/otelrag"
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
}

// Service is the only unit that holds the rag backend.
type Service struct {
	wrapper    *otelrag.Wrapper
	chunkStore *ragpostgres.Store
	tracer     trace.Tracer
}

// New wires the adapters, rag.System, and the otelrag wrapper.
func New(d Deps) *Service {
	sys := ragcore.New(ragcore.Options{
		Model:    ragModelAdapter{inner: d.Model},
		Embedder: ragEmbedderAdapter{inner: d.Embedder},
		Store:    d.RagStore, // nil → rag default in-memory store (unit tests)
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
	return &Service{wrapper: wrapper, chunkStore: d.ChunkStore, tracer: tracer}
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
