package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/costa92/llm-agent-kb/internal/orgkb"
	"github.com/costa92/llm-agent-kb/internal/ragsvc"
	"github.com/costa92/llm-agent-kb/internal/retrieval"
)

// staticKBGetter is a DB-free kbGetter returning a fixed kb with the given
// namespace (so the community/single-doc handlers can be unit-tested).
type staticKBGetter struct{ ns string }

func (s staticKBGetter) Get(context.Context, string) (orgkb.KB, error) {
	return orgkb.KB{ID: "k1", Namespace: s.ns}, nil
}

// fakeAsker satisfies the widened Asker with canned per-mode outputs.
type fakeAsker struct {
	out       retrieval.AskOutput
	globalOut retrieval.AskOutput
	driftOut  retrieval.AskOutput
}

func (f *fakeAsker) Ask(context.Context, retrieval.AskInput) (retrieval.AskOutput, error) {
	return f.out, nil
}
func (f *fakeAsker) AskGlobal(context.Context, retrieval.GlobalInput) (retrieval.AskOutput, error) {
	return f.globalOut, nil
}
func (f *fakeAsker) AskDrift(context.Context, retrieval.DriftInput) (retrieval.AskOutput, error) {
	return f.driftOut, nil
}

// fakeCommunityReader satisfies CommunityReader on kb-local DTOs (no rag/graph).
type fakeCommunityReader struct {
	communities []ragsvc.CommunityView
	report      ragsvc.CommunityReportView
	reportOK    bool
}

func (f *fakeCommunityReader) ListCommunities(context.Context, string) ([]ragsvc.CommunityView, error) {
	return f.communities, nil
}
func (f *fakeCommunityReader) CommunityReport(context.Context, string, string) (ragsvc.CommunityReportView, bool, error) {
	return f.report, f.reportOK, nil
}

func TestListCommunitiesHandler(t *testing.T) {
	cr := &fakeCommunityReader{communities: []ragsvc.CommunityView{{ID: "c1", Level: 0}, {ID: "c2", Level: 1}}}
	h := listCommunitiesHandler(staticKBGetter{ns: "kb_k1"}, cr)
	req := httptest.NewRequest("GET", "/api/kb/k1/communities", nil)
	req.SetPathValue("id", "k1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Items) != 2 {
		t.Fatalf("items=%d want 2", len(out.Items))
	}
}

func TestCommunityReportHandler(t *testing.T) {
	cr := &fakeCommunityReader{report: ragsvc.CommunityReportView{ID: "c1", Title: "Theme", Summary: "S"}, reportOK: true}
	h := communityReportHandler(staticKBGetter{ns: "kb_k1"}, cr)
	req := httptest.NewRequest("GET", "/api/kb/k1/communities/c1", nil)
	req.SetPathValue("id", "k1")
	req.SetPathValue("cid", "c1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	// missing report → 404
	cr.reportOK = false
	rec2 := httptest.NewRecorder()
	h(rec2, req)
	if rec2.Code != 404 {
		t.Fatalf("missing report code=%d want 404", rec2.Code)
	}
}

func TestGetDocHandler(t *testing.T) {
	reader := fakeDocStatus{status: "ready", phase: "done", chunkCount: 3}
	h := getDocHandler(staticKBGetter{ns: "kb_k1"}, reader)
	req := httptest.NewRequest("GET", "/api/kb/k1/documents/d1", nil)
	req.SetPathValue("id", "k1")
	req.SetPathValue("docId", "d1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["id"] != "d1" || out["status"] != "ready" {
		t.Fatalf("out=%v", out)
	}
}

// fakeDocStatus satisfies DocStatusReader for the single-doc GET test.
type fakeDocStatus struct {
	status, phase string
	chunkCount    int
	errMsg        string
}

func (f fakeDocStatus) DocumentStatus(context.Context, string, string) (string, string, int, string, error) {
	return f.status, f.phase, f.chunkCount, f.errMsg, nil
}
