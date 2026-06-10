package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kbeval "github.com/costa92/llm-agent-kb/internal/eval"
	"github.com/costa92/llm-agent-kb/internal/limits"
	"github.com/costa92/llm-agent-kb/internal/sessions"
)

// fakeEvalRunner satisfies EvalRunner.
type fakeEvalRunner struct {
	lastKind kbeval.Kind
	lastNS   string
}

func (f *fakeEvalRunner) RunEval(ctx context.Context, kbID, namespace string, kind kbeval.Kind, datasetJSONL []byte) (kbeval.EvalResult, string, error) {
	f.lastKind = kind
	f.lastNS = namespace
	mv := kbeval.MetricsView{PrecisionAtK: 0.5, Examples: 1, TopK: 5}
	return kbeval.EvalResult{Kind: kind, DatasetName: "ds", Retrieval: &mv}, "run123", nil
}
func (f *fakeEvalRunner) ListRuns(ctx context.Context, kbID string, limit int, cursor string) ([]kbeval.RunRow, string, error) {
	return []kbeval.RunRow{{ID: "run123", Kind: "retrieval", DatasetName: "ds"}}, "", nil
}

// fakeSessionReader satisfies SessionReader.
type fakeSessionReader struct{}

func (fakeSessionReader) ListByKB(ctx context.Context, kbID string, limit int, cursor string) ([]sessions.Session, string, error) {
	return []sessions.Session{{ID: "s1", Title: "fox?"}}, "", nil
}
func (fakeSessionReader) Transcript(ctx context.Context, kbID, sessionID string) ([]sessions.Message, error) {
	return []sessions.Message{{ID: "m1", Role: "user", Content: "fox?"}, {ID: "m2", Role: "assistant", Content: "the fox", Mode: "hybrid"}}, nil
}

func TestEvalRunHandler(t *testing.T) {
	runner := &fakeEvalRunner{}
	h := evalRunHandler(staticKBGetter{ns: "kb_x"}, runner, mustGuard())
	req := httptest.NewRequest("POST", "/api/kb/x/eval/run", strings.NewReader(`{"kind":"retrieval","dataset":"{\"query\":\"q\",\"top_k\":5}"}`))
	req.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	h(w, req) // no uid set: unlimited guard ignores the empty key (authzhttp has no WithUserID)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s want 200", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["runId"] != "run123" {
		t.Fatalf("runId = %v", body["runId"])
	}
	if runner.lastNS != "kb_x" {
		t.Fatalf("namespace forced = %q, want kb_x", runner.lastNS)
	}
}

func TestEvalRunHandlerRejectsBadKind(t *testing.T) {
	h := evalRunHandler(staticKBGetter{ns: "kb_x"}, &fakeEvalRunner{}, mustGuard())
	req := httptest.NewRequest("POST", "/api/kb/x/eval/run", strings.NewReader(`{"kind":"bogus","dataset":"{}"}`))
	req.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	h(w, req) // no uid set: unlimited guard ignores the empty key (authzhttp has no WithUserID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d want 400", w.Code)
	}
}

func TestListRunsHandler(t *testing.T) {
	h := listRunsHandler(staticKBGetter{ns: "kb_x"}, &fakeEvalRunner{})
	req := httptest.NewRequest("GET", "/api/kb/x/eval/runs", nil)
	req.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["items"]; !ok {
		t.Fatalf("missing items envelope: %s", w.Body.String())
	}
}

func TestSessionTranscriptHandler(t *testing.T) {
	h := sessionTranscriptHandler(staticKBGetter{ns: "kb_x"}, fakeSessionReader{})
	req := httptest.NewRequest("GET", "/api/kb/x/sessions/s1", nil)
	req.SetPathValue("id", "x")
	req.SetPathValue("sid", "s1")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"messages"`) {
		t.Fatalf("missing messages: %s", w.Body.String())
	}
}

func mustGuard() *limits.Guard { return limits.New(0) } // 0 = unlimited
