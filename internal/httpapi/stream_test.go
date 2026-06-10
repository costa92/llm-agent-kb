package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/costa92/llm-agent-kb/internal/retrieval"
)

// fakeStreamAsker satisfies Asker; only AskStream is exercised here.
type fakeStreamAsker struct {
	gotInput  retrieval.AskInput
	tokens    []string
	done      retrieval.StreamDone
	streamErr error
}

func (f *fakeStreamAsker) Ask(context.Context, retrieval.AskInput) (retrieval.AskOutput, error) {
	return retrieval.AskOutput{}, nil
}
func (f *fakeStreamAsker) AskGlobal(context.Context, retrieval.GlobalInput) (retrieval.AskOutput, error) {
	return retrieval.AskOutput{}, nil
}
func (f *fakeStreamAsker) AskDrift(context.Context, retrieval.DriftInput) (retrieval.AskOutput, error) {
	return retrieval.AskOutput{}, nil
}
func (f *fakeStreamAsker) AskStream(_ context.Context, in retrieval.AskInput, cb retrieval.StreamCallback) error {
	f.gotInput = in
	if f.streamErr != nil {
		return f.streamErr
	}
	for _, tok := range f.tokens {
		if err := cb.OnToken(tok); err != nil {
			return err
		}
	}
	return cb.OnDone(f.done)
}

func TestAskStreamHandlerEmitsSSE(t *testing.T) {
	asker := &fakeStreamAsker{
		tokens: []string{"hello ", "world"},
		done: retrieval.StreamDone{
			Citations:   []retrieval.Citation{{ChunkID: "c1", DocID: "d1", Title: "Doc", Score: 0.9, Snippet: "snip"}},
			Diagnostics: map[string]any{"mode": "hybrid", "hitCount": 1},
			SessionID:   "sid-1",
		},
	}
	// Drive the bare handler (no auth chain) — RBAC is covered by the
	// route-wiring + e2e; this isolates the SSE framing. PathValue("id") is
	// set via SetPathValue so the handler builds namespace "kb_demo".
	req := httptest.NewRequest("POST", "/api/kb/demo/ask/stream", strings.NewReader(`{"q":"fox","mode":"hybrid","topK":5}`))
	req.SetPathValue("id", "demo")
	rec := httptest.NewRecorder()
	askStreamHandler(asker)(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: token\ndata: {\"text\":\"hello \"}\n\n") {
		t.Fatalf("missing first token frame; body=\n%s", body)
	}
	if !strings.Contains(body, "event: token\ndata: {\"text\":\"world\"}\n\n") {
		t.Fatalf("missing second token frame; body=\n%s", body)
	}
	if !strings.Contains(body, "event: done\ndata: ") || !strings.Contains(body, "\"sessionId\":\"sid-1\"") {
		t.Fatalf("missing/incomplete done frame; body=\n%s", body)
	}
	if !strings.Contains(body, "\"citations\":[") || !strings.Contains(body, "\"chunkId\":\"c1\"") {
		t.Fatalf("done frame missing citations; body=\n%s", body)
	}
	if asker.gotInput.Namespace != "kb_demo" {
		t.Fatalf("namespace = %q, want kb_demo", asker.gotInput.Namespace)
	}
}

func TestAskStreamHandlerBadBodyReturns400(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/kb/demo/ask/stream", strings.NewReader(`not json`))
	req.SetPathValue("id", "demo")
	rec := httptest.NewRecorder()
	askStreamHandler(&fakeStreamAsker{})(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}
