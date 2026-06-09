package ragsvc

import (
	"context"
	"testing"

	"github.com/costa92/llm-agent-contract/llm"
	ragembed "github.com/costa92/llm-agent-rag/embed"
	raggenerate "github.com/costa92/llm-agent-rag/generate"
)

func TestModelAdapterMapsRequestAndUsage(t *testing.T) {
	scripted := llm.NewScriptedLLM(llm.WithResponses(llm.Response{
		Text:  "the answer",
		Usage: llm.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
	}))
	m := ragModelAdapter{inner: scripted}
	resp, err := m.Generate(context.Background(), raggenerate.Request{
		SystemPrompt: "be terse",
		Messages:     []raggenerate.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "the answer" {
		t.Fatalf("Text=%q want 'the answer'", resp.Text)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.CompletionTokens != 3 || resp.Usage.TotalTokens != 10 {
		t.Fatalf("Usage=%+v want {7 3 10}", resp.Usage)
	}
}

func TestModelAdapterFillsTotalWhenZero(t *testing.T) {
	scripted := llm.NewScriptedLLM(llm.WithResponses(llm.Response{
		Text:  "x",
		Usage: llm.Usage{InputTokens: 4, OutputTokens: 6, TotalTokens: 0},
	}))
	m := ragModelAdapter{inner: scripted}
	resp, _ := m.Generate(context.Background(), raggenerate.Request{})
	if resp.Usage.TotalTokens != 10 {
		t.Fatalf("TotalTokens=%d want 10 (derived from 4+6)", resp.Usage.TotalTokens)
	}
}

func TestEmbedderAdapterEmbedAndDimension(t *testing.T) {
	scripted := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	a := ragEmbedderAdapter{inner: scripted}
	if a.Dimension() != 8 {
		t.Fatalf("Dimension=%d want 8", a.Dimension())
	}
	v, err := a.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(v) != 8 {
		t.Fatalf("len(vector)=%d want 8", len(v))
	}
}

func TestEmbedderAdapterBatchPreservesOrder(t *testing.T) {
	scripted := llm.NewScriptedLLM(llm.WithEmbedDimensions(4))
	var _ ragembed.BatchEmbedder = ragEmbedderAdapter{inner: scripted}
	a := ragEmbedderAdapter{inner: scripted}
	vecs, err := a.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("len=%d want 3", len(vecs))
	}
}
