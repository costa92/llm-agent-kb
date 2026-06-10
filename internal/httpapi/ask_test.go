package httpapi

import (
	"encoding/json"
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
