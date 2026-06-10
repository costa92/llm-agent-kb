package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/costa92/llm-agent-kb/internal/retrieval"
)

func TestAskGlobalHandler(t *testing.T) {
	asker := &fakeAsker{globalOut: retrieval.AskOutput{Answer: "G", Citations: []retrieval.Citation{}, Diagnostics: map[string]any{"mode": "global"}}}
	h := askGlobalHandler(asker)
	req := httptest.NewRequest("POST", "/api/kb/k1/ask/global", strings.NewReader(`{"q":"themes?","maxCommunities":4}`))
	req.SetPathValue("id", "k1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["answer"] != "G" {
		t.Fatalf("answer=%v", out["answer"])
	}
}

func TestAskDriftHandler(t *testing.T) {
	asker := &fakeAsker{driftOut: retrieval.AskOutput{Answer: "D", Citations: []retrieval.Citation{}, Diagnostics: map[string]any{"mode": "drift"}}}
	h := askDriftHandler(asker)
	req := httptest.NewRequest("POST", "/api/kb/k1/ask/drift", strings.NewReader(`{"q":"detail?","maxCommunities":4,"rounds":2,"topK":8}`))
	req.SetPathValue("id", "k1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["answer"] != "D" {
		t.Fatalf("answer=%v", out["answer"])
	}
}

func TestAskHandlerThreadsSessionFields(t *testing.T) {
	asker := &fakeAsker{out: retrieval.AskOutput{Answer: "a", SessionID: "sess1"}}
	h := askHandler(asker)
	req := httptest.NewRequest("POST", "/api/kb/x/ask", strings.NewReader(`{"q":"fox","mode":"hybrid","topK":5,"sessionId":"s9"}`))
	req.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if asker.gotAsk.KBID != "x" {
		t.Fatalf("KBID = %q, want x", asker.gotAsk.KBID)
	}
	if asker.gotAsk.SessionID != "s9" {
		t.Fatalf("SessionID = %q, want s9", asker.gotAsk.SessionID)
	}
	// UserID comes from authzhttp.UserID(ctx) — empty outside the auth chain, which
	// is correct: the handler must read it from context, not the body.
	if asker.gotAsk.Namespace != "kb_x" {
		t.Fatalf("Namespace = %q, want kb_x", asker.gotAsk.Namespace)
	}
}
