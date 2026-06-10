package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	authzhttp "github.com/costa92/llm-agent-authz/httpapi"

	"github.com/costa92/llm-agent-kb/internal/ingest"
	"github.com/costa92/llm-agent-kb/internal/orgkb"
	"github.com/costa92/llm-agent-kb/internal/retrieval"
)

// uploadHandler enqueues a document for async ingest (spec §6: 202 +
// documentId). It applies the §16.3 upload validation (extension allowlist for
// file sources) and the per-kb storage quota BEFORE enqueueing — validation
// precedes quota precedes enqueue. The repo lookup is taken as a kbGetter so
// the handler is unit-testable DB-free.
func uploadHandler(repo kbGetter, ing Ingester, maxUpload, quota int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var req struct {
			Title      string `json:"title"`
			SourceType string `json:"sourceType"`
			Content    string `json:"content"`
			URL        string `json:"url"`      // for sourceType=url
			Filename   string `json:"filename"` // for file uploads (extension allowlist)
		}
		body := http.MaxBytesReader(w, r.Body, maxUpload)
		raw, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		st := ingest.SourceType(req.SourceType)
		content := []byte(req.Content)
		sourceRef := ""
		switch st {
		case ingest.SourceTypeURL:
			sourceRef = req.URL
			if sourceRef == "" {
				http.Error(w, "url required for sourceType=url", http.StatusBadRequest)
				return
			}
		case ingest.SourceTypePDF, ingest.SourceTypeDOCX:
			// File bytes arrive base64-decoded by the client into Content; validate
			// the declared filename's extension + size.
			if err := ingest.ValidateUpload(req.Filename, int64(len(content)), maxUpload); err != nil {
				http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
				return
			}
			sourceRef = req.Filename
		}
		// Per-kb storage quota (§16.3).
		if used, err := ing.KBContentBytes(r.Context(), kb.ID); err == nil && used+int64(len(content)) > quota {
			http.Error(w, "kb storage quota exceeded", http.StatusInsufficientStorage)
			return
		}
		docID, err := ing.Enqueue(r.Context(), ingest.IngestInput{
			KBID: kb.ID, Namespace: kb.Namespace, Title: req.Title,
			SourceType: st, SourceRef: sourceRef, Raw: content,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"documentId": docID, "status": "pending"})
	}
}

// listDocsHandler returns up to limit documents for a kb, keyset-paginated by
// id, in the §16.2 cursor envelope {items, next_cursor} (viewer+).
func listDocsHandler(repo kbGetter, ing Ingester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		limit := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		items, next, err := ing.ListDocuments(r.Context(), kb.ID, limit, r.URL.Query().Get("cursor"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]ingest.DocumentView, 0, len(items))
		out = append(out, items...)
		writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
	}
}

// retryHandler re-enqueues a dead document's job (editor+, spec §16.2).
func retryHandler(repo kbGetter, ing Ingester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := ing.Retry(r.Context(), kb.ID, r.PathValue("docId")); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"documentId": r.PathValue("docId"), "status": "pending"})
	}
}

// progressHandler streams document status as Server-Sent Events until the
// document reaches a terminal state (ready/failed) or the client disconnects.
func progressHandler(repo kbGetter, reader DocStatusReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		docID := r.PathValue("docId")
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		emit := func() (terminal bool) {
			status, phase, cc, errMsg, err := reader.DocumentStatus(r.Context(), kb.ID, docID)
			if err != nil {
				_, _ = io.WriteString(w, "event: error\ndata: {\"error\":\"not found\"}\n\n")
				flusher.Flush()
				return true
			}
			payload, _ := json.Marshal(map[string]any{"status": status, "phase": phase, "chunkCount": cc, "error": errMsg})
			_, _ = io.WriteString(w, "data: "+string(payload)+"\n\n")
			flusher.Flush()
			return status == "ready" || status == "failed"
		}
		if emit() {
			return
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if emit() {
					return
				}
			}
		}
	}
}

func deleteDocHandler(repo *orgkb.Repo, ing Ingester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := ing.DeleteDocument(r.Context(), kb.Namespace, r.PathValue("docId")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getDocHandler returns a single document's status snapshot (viewer+, §16.2):
// {id, status, phase, chunkCount, error}. Uses the same DocStatusReader as the
// SSE progress endpoint, keyed by kb.ID + docId.
func getDocHandler(repo kbGetter, reader DocStatusReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		docID := r.PathValue("docId")
		status, phase, cc, errMsg, err := reader.DocumentStatus(r.Context(), kb.ID, docID)
		if err != nil {
			http.Error(w, "document not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": docID, "status": status, "phase": phase, "chunkCount": cc, "error": errMsg,
		})
	}
}

// listCommunitiesHandler lists the GraphRAG communities for a kb (viewer+, M3).
// The CommunityReader returns kb-local DTOs (CommunityView) — httpapi serializes
// them to lowercase JSON without importing rag/graph (spec §4).
func listCommunitiesHandler(repo kbGetter, cr CommunityReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		comms, err := cr.ListCommunities(r.Context(), kb.Namespace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := make([]map[string]any, 0, len(comms))
		for _, c := range comms {
			items = append(items, map[string]any{
				"id": c.ID, "level": c.Level, "parentId": c.ParentID, "entityCount": c.EntityCount,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

// communityReportHandler returns a single community's report (viewer+, M3).
// A missing report (ok==false) is a 404 — distinct from an internal error.
func communityReportHandler(repo kbGetter, cr CommunityReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rep, ok, err := cr.CommunityReport(r.Context(), kb.Namespace, r.PathValue("cid"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "community report not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": rep.ID, "title": rep.Title, "summary": rep.Summary,
		})
	}
}

func getKBHandler(repo *orgkb.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": kb.ID, "orgId": kb.OrgID, "name": kb.Name, "namespace": kb.Namespace,
		})
	}
}

// createOrgHandler (POST /api/orgs) is the bootstrap seam: any authenticated
// user creates an org and is written as that org's org_admin (orgkb.CreateOrg).
func createOrgHandler(repo *orgkb.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := authzhttp.UserID(r.Context())
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "bad request: name required", http.StatusBadRequest)
			return
		}
		orgID, err := repo.CreateOrg(r.Context(), req.Name, uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": orgID, "name": req.Name})
	}
}

// createKBHandler (POST /api/orgs/{org}/kbs) requires org-level admin. Create
// writes the creator's kb-scope admin membership (orgkb.Create).
func createKBHandler(repo *orgkb.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := authzhttp.UserID(r.Context())
		var req struct {
			Name           string `json:"name"`
			EmbeddingModel string `json:"embeddingModel"`
			EmbeddingDim   int    `json:"embeddingDim"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "bad request: name required", http.StatusBadRequest)
			return
		}
		kb, err := repo.Create(r.Context(), orgkb.CreateInput{
			OrgID:          r.PathValue("org"),
			Name:           req.Name,
			CreatorUserID:  uid,
			EmbeddingModel: req.EmbeddingModel,
			EmbeddingDim:   req.EmbeddingDim,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": kb.ID, "orgId": kb.OrgID, "name": kb.Name, "namespace": kb.Namespace,
		})
	}
}

// listKBHandler (GET /api/orgs/{org}/kbs) requires org-level viewer+. Returns
// the §16.2 cursor envelope {items, next_cursor}.
func listKBHandler(repo *orgkb.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		items, next, err := repo.ListByOrg(r.Context(), r.PathValue("org"), limit, r.URL.Query().Get("cursor"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]map[string]any, 0, len(items))
		for _, kb := range items {
			out = append(out, map[string]any{
				"id": kb.ID, "orgId": kb.OrgID, "name": kb.Name, "namespace": kb.Namespace,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
	}
}

// deleteKBHandler applies the §16.4 delete-kb cascade in strict order:
// (1) delete every document in the kb (chunks + graph reconcile, via the
// widened Ingester), then (2) repo.DeleteRow, which removes the knowledge_base
// row AND the kb-scope auth_membership rows in one transaction (Task 6).
func deleteKBHandler(repo *orgkb.Repo, ing Ingester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, orgkb.ErrNotFound) {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// 1. Cascade-delete all documents (chunks + graph) for the kb.
		if err := ing.DeleteAllDocumentsForKB(r.Context(), kb.Namespace, kb.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// 2. Delete the kb row + its kb-scope memberships (one tx, §16.4).
		if err := repo.DeleteRow(r.Context(), kb.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// askStreamHandler streams a grounded answer as Server-Sent Events (M5a,
// viewer+). Wire contract: zero+ `event: token` frames ({"text":...}) then a
// terminal `event: done` ({citations,diagnostics,sessionId}) or `event: error`.
// Reuses the progressHandler SSE pattern: Flusher + text/event-stream headers,
// flush per frame, client disconnect via r.Context() (plumbed through AskStream
// → StreamAnswer → model.Stream). Body/mode errors before the first frame are
// a normal HTTP 400; failures after headers are sent become an error frame.
func askStreamHandler(asker Asker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Q         string `json:"q"`
			Mode      string `json:"mode"`
			TopK      int    `json:"topK"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		in := retrieval.AskInput{
			Namespace: "kb_" + r.PathValue("id"),
			KBID:      r.PathValue("id"),
			UserID:    authzhttp.UserID(r.Context()),
			SessionID: req.SessionID,
			Question:  req.Q,
			Mode:      req.Mode,
			TopK:      req.TopK,
		}
		writeFrame := func(event string, payload any) error {
			data, _ := json.Marshal(payload)
			if _, err := io.WriteString(w, "event: "+event+"\ndata: "+string(data)+"\n\n"); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		err := asker.AskStream(r.Context(), in, retrieval.StreamCallback{
			OnToken: func(text string) error {
				return writeFrame("token", map[string]any{"text": text})
			},
			OnDone: func(d retrieval.StreamDone) error {
				return writeFrame("done", map[string]any{
					"citations":   d.Citations,
					"diagnostics": d.Diagnostics,
					"sessionId":   d.SessionID,
				})
			},
		})
		if err != nil {
			// Headers are already sent (text/event-stream); surface the failure
			// as an SSE error frame rather than a (now-impossible) HTTP status.
			_ = writeFrame("error", map[string]any{"error": err.Error()})
		}
	}
}
