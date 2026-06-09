package ragsvc

import (
	"context"

	"github.com/costa92/llm-agent-contract/llm"
	ragembed "github.com/costa92/llm-agent-rag/embed"
	raggenerate "github.com/costa92/llm-agent-rag/generate"
)

// ragModelAdapter adapts a contract llm.ChatModel into the rag generate.Model
// seam. Hand-written because the shipped adapter/llmagent is build-tagged and
// would pull the llm-agent core (spec §12.1).
type ragModelAdapter struct {
	inner llm.ChatModel
}

func (a ragModelAdapter) Generate(ctx context.Context, req raggenerate.Request) (raggenerate.Response, error) {
	msgs := make([]llm.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	resp, err := a.inner.Generate(ctx, llm.Request{
		SystemPrompt: req.SystemPrompt,
		Messages:     msgs,
		Metadata:     req.Metadata,
	})
	if err != nil {
		return raggenerate.Response{}, err
	}
	total := resp.Usage.TotalTokens
	if total == 0 {
		total = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}
	return raggenerate.Response{
		Text: resp.Text,
		Usage: raggenerate.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      total,
		},
	}, nil
}

// ragEmbedderAdapter adapts a contract llm.Embedder into the rag embed.Embedder
// + embed.BatchEmbedder seams (mirrors customer-support/internal/knowledgebase).
// Implementing EmbedBatch engages rag.System.Import's batch fast path.
type ragEmbedderAdapter struct {
	inner llm.Embedder
}

func (a ragEmbedderAdapter) Embed(ctx context.Context, text string) (ragembed.Vector, error) {
	vectors, _, err := a.inner.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, nil
	}
	return ragembed.Vector(vectors[0]), nil
}

func (a ragEmbedderAdapter) Dimension() int { return a.inner.EmbedDimensions() }

func (a ragEmbedderAdapter) EmbedBatch(ctx context.Context, texts []string) ([]ragembed.Vector, error) {
	vectors, _, err := a.inner.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	out := make([]ragembed.Vector, len(vectors))
	for i, v := range vectors {
		out[i] = ragembed.Vector(v)
	}
	return out, nil
}
