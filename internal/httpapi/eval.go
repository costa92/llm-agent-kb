package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	authzhttp "github.com/costa92/llm-agent-authz/httpapi"

	kbeval "github.com/costa92/llm-agent-kb/internal/eval"
	"github.com/costa92/llm-agent-kb/internal/limits"
	"github.com/costa92/llm-agent-kb/internal/orgkb"
	"github.com/costa92/llm-agent-kb/internal/sessions"
)

// EvalRunner is the eval surface (satisfied by *eval.Runner — Task 11 wires it).
// RunEval forces the kb namespace; ListRuns is cursor-paginated.
type EvalRunner interface {
	RunEval(ctx context.Context, kbID, namespace string, kind kbeval.Kind, datasetJSONL []byte) (kbeval.EvalResult, string, error)
	ListRuns(ctx context.Context, kbID string, limit int, cursor string) ([]kbeval.RunRow, string, error)
}

// SessionReader is the read surface for Q&A history (satisfied by *sessions.Repo).
type SessionReader interface {
	ListByKB(ctx context.Context, kbID string, limit int, cursor string) ([]sessions.Session, string, error)
	Transcript(ctx context.Context, kbID, sessionID string) ([]sessions.Message, error)
}

// validEvalKind reports whether k is one of the four supported kinds.
func validEvalKind(k kbeval.Kind) bool {
	switch k {
	case kbeval.KindRetrieval, kbeval.KindTriad, kbeval.KindGlobal, kbeval.KindDrift:
		return true
	}
	return false
}

// userIDFromCtx returns the authenticated user id (empty outside the auth chain).
func userIDFromCtx(r *http.Request) string { return authzhttp.UserID(r.Context()) }

// evalRunHandler runs an eval (editor+, §16.2). Body: {kind, datasetName?,
// dataset}. dataset is inline JSONL (one Example per line; LoadDataset parses).
// The handler enforces a SEPARATE per-user eval-run budget (eval is expensive).
func evalRunHandler(repo kbGetter, runner EvalRunner, evalGuard *limits.Guard) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := userIDFromCtx(r)
		if !evalGuard.Allow(uid) {
			http.Error(w, "eval-run rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var req struct {
			Kind    string `json:"kind"`
			Dataset string `json:"dataset"` // inline JSONL
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		kind := kbeval.Kind(req.Kind)
		if !validEvalKind(kind) {
			http.Error(w, "unsupported eval kind (retrieval|triad|global|drift)", http.StatusBadRequest)
			return
		}
		if req.Dataset == "" {
			http.Error(w, "dataset (inline JSONL) required", http.StatusBadRequest)
			return
		}
		res, runID, err := runner.RunEval(r.Context(), kb.ID, kb.Namespace, kind, []byte(req.Dataset))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runId": runID, "result": res})
	}
}

// listRunsHandler lists eval runs for a kb (viewer+, cursor envelope).
func listRunsHandler(repo kbGetter, runner EvalRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		limit := parseLimit(r)
		rows, next, err := runner.ListRuns(r.Context(), kb.ID, limit, r.URL.Query().Get("cursor"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			item := map[string]any{
				"id": row.ID, "kind": row.Kind, "datasetName": row.DatasetName,
				"createdAt": row.CreatedAt, "metrics": json.RawMessage(row.MetricsJSON),
			}
			if len(row.DriftJSON) > 0 {
				item["drift"] = json.RawMessage(row.DriftJSON)
			}
			items = append(items, item)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
	}
}

// listSessionsHandler lists Q&A sessions for a kb (viewer+, cursor envelope).
func listSessionsHandler(repo kbGetter, reader SessionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		limit := parseLimit(r)
		rows, next, err := reader.ListByKB(r.Context(), kb.ID, limit, r.URL.Query().Get("cursor"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := make([]map[string]any, 0, len(rows))
		for _, s := range rows {
			items = append(items, map[string]any{"id": s.ID, "title": s.Title, "createdAt": s.CreatedAt})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
	}
}

// sessionTranscriptHandler returns a session's messages (viewer+). A session
// belonging to another kb resolves to 404 (sessions.ErrNotFound).
func sessionTranscriptHandler(repo kbGetter, reader SessionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		msgs, err := reader.Transcript(r.Context(), kb.ID, r.PathValue("sid"))
		if errors.Is(err, sessions.ErrNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := make([]map[string]any, 0, len(msgs))
		for _, m := range msgs {
			item := map[string]any{"id": m.ID, "role": m.Role, "content": m.Content, "mode": m.Mode, "createdAt": m.CreatedAt}
			if len(m.CitationsJSON) > 0 {
				item["citations"] = json.RawMessage(m.CitationsJSON)
			}
			items = append(items, item)
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessionId": r.PathValue("sid"), "messages": items})
	}
}

// parseLimit reads ?limit= (0 = repo default).
func parseLimit(r *http.Request) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}
