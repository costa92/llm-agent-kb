package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	authzhttp "github.com/costa92/llm-agent-authz/httpapi"

	"github.com/costa92/llm-agent-kb/internal/ingest"
	"github.com/costa92/llm-agent-kb/internal/orgkb"
)

// maxUploadBytes caps a single document body in M1 (full upload validation is M2).
const maxUploadBytes = 10 << 20 // 10 MiB

func uploadHandler(repo *orgkb.Repo, ing Ingester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		}
		// JSON paste/markdown/txt body: {title, sourceType, content}. (multipart
		// file upload is wired the same way; M1 accepts the JSON shape.)
		var req struct {
			Title      string `json:"title"`
			SourceType string `json:"sourceType"`
			Content    string `json:"content"`
		}
		body := http.MaxBytesReader(w, r.Body, maxUploadBytes)
		raw, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res, err := ing.Ingest(r.Context(), ingest.IngestInput{
			KBID:       kb.ID,
			Namespace:  kb.Namespace,
			Title:      req.Title,
			SourceType: ingest.SourceType(req.SourceType),
			Raw:        []byte(req.Content),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"documentId": res.DocumentID, "status": res.Status, "chunkCount": res.ChunkCount,
		})
	}
}

func deleteDocHandler(repo *orgkb.Repo, ing Ingester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "kb not found", http.StatusNotFound)
			return
		}
		if err := ing.DeleteDocument(r.Context(), kb.Namespace, r.PathValue("docId")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func getKBHandler(repo *orgkb.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kb, err := repo.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "kb not found", http.StatusNotFound)
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
		if err != nil {
			http.Error(w, "kb not found", http.StatusNotFound)
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
