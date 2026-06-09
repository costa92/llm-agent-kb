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

	"go.opentelemetry.io/otel/trace"
)

// AskRequest is the kb-side ask request. Hybrid=false is vector mode
// (lexical/rerank off); Hybrid=true enables reranking. NO global/drift in M1.
type AskRequest struct {
	Namespace      string
	TopK           int
	Hybrid         bool
	MaxTotalTokens int
}

// RagPort is the narrow rag surface every non-rag kb package depends on.
// M1: Ask + Import + the three delete primitives (§16.4). No AskGlobal/
// AskDrift/Prewarm — those arrive in M3.
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
	return &Service{wrapper: wrapper, chunkStore: d.ChunkStore}
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
