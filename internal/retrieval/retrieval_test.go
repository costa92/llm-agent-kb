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
	gotReq    ragsvc.AskRequest
	answer    ragcore.Answer
	globalAns ragcore.Answer
	driftAns  ragcore.Answer
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
func (f *fakeRag) Retrieve(ctx context.Context, query string, opts ragcore.SearchOptions) ([]ragstore.Hit, error) {
	return nil, nil
}

// M3 RagPort surface — global/drift return canned answers for the mapping tests.
func (f *fakeRag) AskGlobal(_ context.Context, _ string, _ ragsvc.GlobalRequest) (ragcore.Answer, error) {
	return f.globalAns, nil
}
func (f *fakeRag) AskDrift(_ context.Context, _ string, _ ragsvc.DriftRequest) (ragcore.Answer, error) {
	return f.driftAns, nil
}
func (f *fakeRag) PrewarmCommunityReports(context.Context, string) (int, error) { return 0, nil }
func (f *fakeRag) RecomputeCommunities(context.Context, string) error           { return nil }
func (f *fakeRag) ListCommunities(context.Context, string) ([]ragsvc.CommunityView, error) {
	return nil, nil
}
func (f *fakeRag) CommunityReport(context.Context, string, string) (ragsvc.CommunityReportView, bool, error) {
	return ragsvc.CommunityReportView{}, false, nil
}

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

// fakeRecorder records the persistence calls.
type fakeRecorder struct {
	ensured  bool
	appended bool
	gotMode  string
	sid      string
}

func (f *fakeRecorder) EnsureSession(ctx context.Context, kbID, userID, sessionID, firstQuestion string) (string, error) {
	f.ensured = true
	f.sid = "sess1"
	return f.sid, nil
}
func (f *fakeRecorder) AppendPair(ctx context.Context, sessionID, question, answer string, citationsJSON []byte, mode string) error {
	f.appended = true
	f.gotMode = mode
	return nil
}

func TestAskPersistsSession(t *testing.T) {
	rec := &fakeRecorder{}
	fake := &fakeRag{answer: ragcore.Answer{
		Text: "the answer",
		Hits: []ragstore.Hit{{Chunk: ragstore.StoredChunk{ID: "c1", Content: "snippet"}, Score: 0.9}},
		Citations: []ragcore.Citation{{
			ChunkID: "c1", DocID: "d1", Title: "Doc One", Score: 0.9,
		}},
		Diagnostics: ragcore.Diagnostics{HitCount: 1},
	}}
	svc := New(fake, Config{})
	svc.SetRecorder(rec)
	out, err := svc.Ask(context.Background(), AskInput{
		Namespace: "kb_x", KBID: "x", UserID: "u1", Question: "fox?", Mode: "hybrid", TopK: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.SessionID != "sess1" {
		t.Fatalf("SessionID = %q, want sess1", out.SessionID)
	}
	if !rec.ensured || !rec.appended {
		t.Fatalf("recorder not called: ensured=%v appended=%v", rec.ensured, rec.appended)
	}
	if rec.gotMode != "hybrid" {
		t.Fatalf("mode = %q", rec.gotMode)
	}
}

func TestAskGlobalMapsDiagnostics(t *testing.T) {
	fake := &fakeRag{globalAns: ragcore.Answer{
		Text: "global answer",
		Diagnostics: ragcore.Diagnostics{
			Global: ragcore.GlobalDiagnostics{
				CommunityIDs: []string{"c1", "c2"}, MapCalls: 2, ReduceCalls: 1,
			},
		},
	}}
	svc := New(fake, Config{GlobalMaxCommunities: 8})
	out, err := svc.AskGlobal(context.Background(), GlobalInput{Namespace: "ns", Question: "themes?", MaxCommunities: 4})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "global answer" {
		t.Fatalf("answer=%q", out.Answer)
	}
	if len(out.Citations) != 0 {
		t.Fatalf("global answers carry no citations, got %d", len(out.Citations))
	}
	if out.Diagnostics["mode"] != "global" {
		t.Fatalf("diagnostics.mode=%v", out.Diagnostics["mode"])
	}
	if out.Diagnostics["mapCalls"] != 2 {
		t.Fatalf("diagnostics.mapCalls=%v want 2", out.Diagnostics["mapCalls"])
	}
}

func TestAskDriftMapsDiagnostics(t *testing.T) {
	fake := &fakeRag{driftAns: ragcore.Answer{
		Text: "drift answer",
		Diagnostics: ragcore.Diagnostics{
			Drift: ragcore.DriftDiagnostics{PrimerCommunityIDs: []string{"c1"}, Rounds: 2},
		},
	}}
	svc := New(fake, Config{DriftRounds: 2, DriftTopK: 5})
	out, err := svc.AskDrift(context.Background(), DriftInput{Namespace: "ns", Question: "detail?", Rounds: 0})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "drift answer" || out.Diagnostics["mode"] != "drift" {
		t.Fatalf("unexpected: %+v", out)
	}
	if out.Diagnostics["rounds"] != 2 {
		t.Fatalf("diagnostics.rounds=%v want 2", out.Diagnostics["rounds"])
	}
}
