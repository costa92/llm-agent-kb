package ragsvc

import (
	"context"
	"testing"

	"github.com/costa92/llm-agent-contract/llm"
	ragstore "github.com/costa92/llm-agent-rag/store"
)

// vec8 is the 8-dim all-0.1 vector — matches the scripted embedder's
// deterministic output (float32(1)/10 per component) so the seeded chunk
// scores cosine-1.0 against any embedded query and the retrieve returns it.
func vec8() []float32 {
	v := make([]float32, 8)
	for i := range v {
		v[i] = 0.1
	}
	return v
}

func TestStreamAnswerEmitsTokensThenDone(t *testing.T) {
	store := ragstore.NewInMemoryStore(8)
	if err := store.Upsert(context.Background(), []ragstore.StoredChunk{
		{ID: "c1", Namespace: "ns1", DocID: "d1", Title: "Doc One", SectionPath: []string{"Intro"}, Content: "the quick brown fox", Vector: vec8()},
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	model := llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "streamed answer", Usage: llm.Usage{TotalTokens: 2}}))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	svc := New(Deps{Model: model, Embedder: embedder, RagStore: store})

	var tokens []string
	var doneCites []StreamCitation
	var doneHitCount int
	var doneSeen bool
	err := svc.StreamAnswer(context.Background(), "fox", StreamRequest{Namespace: "ns1", TopK: 5, Hybrid: false},
		func(ev StreamEvent) error {
			switch ev.Kind {
			case StreamEventToken:
				tokens = append(tokens, ev.Text)
			case StreamEventDone:
				doneSeen = true
				doneCites = ev.Citations
				doneHitCount = ev.HitCount
			}
			return nil
		})
	if err != nil {
		t.Fatalf("StreamAnswer: %v", err)
	}
	if len(tokens) == 0 {
		t.Fatal("expected at least one token event")
	}
	if got := joinTokens(tokens); got != "streamed answer" {
		t.Fatalf("concatenated tokens = %q, want %q", got, "streamed answer")
	}
	if !doneSeen {
		t.Fatal("expected a terminal done event")
	}
	if doneHitCount != 1 {
		t.Fatalf("done HitCount = %d, want 1", doneHitCount)
	}
	if len(doneCites) != 1 || doneCites[0].ChunkID != "c1" || doneCites[0].DocID != "d1" || doneCites[0].Title != "Doc One" {
		t.Fatalf("done citations = %+v, want one c1/d1/Doc One", doneCites)
	}
	if doneCites[0].Snippet != "the quick brown fox" {
		t.Fatalf("citation snippet = %q, want chunk content", doneCites[0].Snippet)
	}
}

func joinTokens(ts []string) string {
	out := ""
	for _, t := range ts {
		out += t
	}
	return out
}
