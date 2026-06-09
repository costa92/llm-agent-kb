package retrieval

import (
	"context"
	"testing"

	ragingest "github.com/costa92/llm-agent-rag/ingest"
	ragcore "github.com/costa92/llm-agent-rag/rag"
	ragstore "github.com/costa92/llm-agent-rag/store"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

type fakeRag struct {
	gotReq ragsvc.AskRequest
	answer ragcore.Answer
}

func (f *fakeRag) Ask(_ context.Context, _ string, req ragsvc.AskRequest) (ragcore.Answer, error) {
	f.gotReq = req
	return f.answer, nil
}
func (f *fakeRag) Import(context.Context, []ragingest.Document, ragingest.ImportOptions) (ragingest.ImportResult, error) {
	return ragingest.ImportResult{}, nil
}
func (f *fakeRag) ListChunkIDs(context.Context, string, string) ([]string, error) { return nil, nil }
func (f *fakeRag) RemoveGraphBySource(context.Context, string, []string) error    { return nil }
func (f *fakeRag) RemoveChunks(context.Context, string, string) (int, error)      { return 0, nil }

func TestAskMapsModeAndCitations(t *testing.T) {
	f := &fakeRag{answer: ragcore.Answer{
		Text: "the answer",
		Hits: []ragstore.Hit{{Chunk: ragstore.StoredChunk{ID: "c1", Content: "a long snippet of source content"}, Score: 0.9}},
		Citations: []ragcore.Citation{{
			ChunkID: "c1", DocID: "d1", Title: "Doc One",
			SectionPath: []string{"Intro"}, Score: 0.9,
		}},
		Diagnostics: ragcore.Diagnostics{HitCount: 1},
	}}
	svc := New(f, Config{MaxAskTokens: 4096, SnippetChars: 10})

	out, err := svc.Ask(context.Background(), AskInput{Namespace: "ns1", Question: "q", Mode: "hybrid", TopK: 5})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !f.gotReq.Hybrid {
		t.Fatal("mode=hybrid must set Hybrid=true (rerank on)")
	}
	if f.gotReq.TopK != 5 || f.gotReq.Namespace != "ns1" || f.gotReq.MaxTotalTokens != 4096 {
		t.Fatalf("ask req=%+v", f.gotReq)
	}
	if out.Answer != "the answer" {
		t.Fatalf("Answer=%q", out.Answer)
	}
	if len(out.Citations) != 1 || out.Citations[0].ChunkID != "c1" || out.Citations[0].DocID != "d1" {
		t.Fatalf("citations=%+v", out.Citations)
	}
	if out.Citations[0].Snippet != "a long sni" { // SnippetChars=10
		t.Fatalf("snippet=%q want first 10 chars", out.Citations[0].Snippet)
	}
	if out.Diagnostics["mode"] != "hybrid" {
		t.Fatalf("diagnostics mode=%v", out.Diagnostics["mode"])
	}
}

func TestVectorModeDisablesRerank(t *testing.T) {
	f := &fakeRag{}
	svc := New(f, Config{MaxAskTokens: 100})
	if _, err := svc.Ask(context.Background(), AskInput{Namespace: "ns", Question: "q", Mode: "vector", TopK: 3}); err != nil {
		t.Fatal(err)
	}
	if f.gotReq.Hybrid {
		t.Fatal("mode=vector must set Hybrid=false")
	}
}

func TestRejectsUnknownMode(t *testing.T) {
	svc := New(&fakeRag{}, Config{})
	if _, err := svc.Ask(context.Background(), AskInput{Mode: "global"}); err == nil {
		t.Fatal("global is M3 — must be rejected in M1")
	}
}
