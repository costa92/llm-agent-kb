package eval

import (
	"context"

	ragcore "github.com/costa92/llm-agent-rag/rag"
	ragstore "github.com/costa92/llm-agent-rag/store"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

// Port is the slice of ragsvc.RagPort the eval adapters call. Narrowing keeps
// the eval use case testable with a tiny fake.
type Port interface {
	Retrieve(ctx context.Context, query string, opts ragcore.SearchOptions) ([]ragstore.Hit, error)
	Ask(ctx context.Context, question string, req ragsvc.AskRequest) (ragcore.Answer, error)
	AskGlobal(ctx context.Context, question string, req ragsvc.GlobalRequest) (ragcore.Answer, error)
	AskDrift(ctx context.Context, question string, req ragsvc.DriftRequest) (ragcore.Answer, error)
}

// retrieverAdapter satisfies eval.Retriever. The evaluator passes rag.SearchOptions
// (TopK + per-example Namespace overlay); we force the kb namespace so an
// uploaded dataset can never escape its tenant.
type retrieverAdapter struct {
	port      Port
	namespace string
}

func (r retrieverAdapter) Retrieve(ctx context.Context, query string, opts ragcore.SearchOptions) ([]ragstore.Hit, error) {
	opts.Namespace = r.namespace
	return r.port.Retrieve(ctx, query, opts)
}

// askerAdapter satisfies eval.Asker (full retrieve+generate). It forces the kb
// namespace + token budget; hybrid (rerank) on for answer quality.
type askerAdapter struct {
	port      Port
	namespace string
	maxTokens int
}

func (a askerAdapter) Ask(ctx context.Context, question string, opts ragcore.AskOptions) (ragcore.Answer, error) {
	return a.port.Ask(ctx, question, ragsvc.AskRequest{
		Namespace:      a.namespace,
		TopK:           opts.Search.TopK,
		Hybrid:         true,
		MaxTotalTokens: a.maxTokens,
	})
}

// globalAskerAdapter satisfies eval.GlobalAsker.
type globalAskerAdapter struct {
	port      Port
	namespace string
	maxTokens int
}

func (g globalAskerAdapter) AskGlobal(ctx context.Context, question string, opts ragcore.GlobalOptions) (ragcore.Answer, error) {
	return g.port.AskGlobal(ctx, question, ragsvc.GlobalRequest{
		Namespace:      g.namespace,
		MaxCommunities: opts.MaxCommunities,
		MaxTotalTokens: g.maxTokens,
	})
}

// driftAskerAdapter satisfies eval.DriftAsker.
type driftAskerAdapter struct {
	port      Port
	namespace string
	maxTokens int
}

func (d driftAskerAdapter) AskDrift(ctx context.Context, question string, opts ragcore.DriftOptions) (ragcore.Answer, error) {
	return d.port.AskDrift(ctx, question, ragsvc.DriftRequest{
		Namespace:      d.namespace,
		MaxCommunities: opts.MaxCommunities,
		Rounds:         opts.Rounds,
		TopK:           opts.TopK,
		MaxTotalTokens: d.maxTokens,
	})
}
