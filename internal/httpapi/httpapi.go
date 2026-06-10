// Package httpapi mounts the kb REST routes (spec §16.2 M1 subset) and wires
// the RBAC middleware chain over authz. It holds no business rules: it
// orchestrates auth + limits + JSON only.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	authzhttp "github.com/costa92/llm-agent-authz/httpapi"
	authzrole "github.com/costa92/llm-agent-authz/role"
	authztoken "github.com/costa92/llm-agent-authz/token"

	"github.com/costa92/llm-agent-kb/internal/ingest"
	"github.com/costa92/llm-agent-kb/internal/limits"
	"github.com/costa92/llm-agent-kb/internal/orgkb"
	"github.com/costa92/llm-agent-kb/internal/ragsvc"
	"github.com/costa92/llm-agent-kb/internal/retrieval"
)

// Asker is the retrieval surface the ask handlers need (satisfied by
// *retrieval.Service). AskGlobal/AskDrift back the GraphRAG endpoints (M3).
type Asker interface {
	Ask(ctx context.Context, in retrieval.AskInput) (retrieval.AskOutput, error)
	AskGlobal(ctx context.Context, in retrieval.GlobalInput) (retrieval.AskOutput, error)
	AskDrift(ctx context.Context, in retrieval.DriftInput) (retrieval.AskOutput, error)
}

// CommunityReader reads the GraphRAG community views (satisfied by
// *ragsvc.Service). Returns kb-local DTOs, NOT rag/graph types — so httpapi
// never imports rag/graph (spec §4: ragsvc is the sole rag/graph importer).
type CommunityReader interface {
	ListCommunities(ctx context.Context, namespace string) ([]ragsvc.CommunityView, error)
	CommunityReport(ctx context.Context, namespace, communityID string) (ragsvc.CommunityReportView, bool, error)
}

// OrgLookup resolves a kb's org (satisfied by *orgkb.Repo).
type OrgLookup interface {
	OrgIDForKB(ctx context.Context, kbID string) (string, error)
}

// Ingester is the document-ingest surface (satisfied by *ingest.Service).
// Enqueue is the M2 async upload path (202 + documentId); the worker drains the
// queue. DeleteAllDocumentsForKB runs the §16.4 cascade over every doc in a kb
// and is used by the delete-kb handler before the kb row + memberships are
// removed. ListDocuments/KBContentBytes/Retry back the M2 document endpoints.
type Ingester interface {
	Enqueue(ctx context.Context, in ingest.IngestInput) (string, error)
	DeleteDocument(ctx context.Context, namespace, documentID string) error
	DeleteAllDocumentsForKB(ctx context.Context, namespace, kbID string) error
	ListDocuments(ctx context.Context, kbID string, limit int, cursor string) ([]ingest.DocumentView, string, error)
	KBContentBytes(ctx context.Context, kbID string) (int64, error)
	Retry(ctx context.Context, kbID, docID string) error
}

// kbGetter is the slice of *orgkb.Repo the document handlers need (just Get).
// Narrowing to an interface lets the handler tests inject a DB-free fake.
type kbGetter interface {
	Get(ctx context.Context, id string) (orgkb.KB, error)
}

// DocStatusReader reads a document's status+phase for progress streaming
// (satisfied by *ingest.Service).
type DocStatusReader interface {
	DocumentStatus(ctx context.Context, kbID, docID string) (status, phase string, chunkCount int, errMsg string, err error)
}

// Deps are the dependencies NewMux wires together.
type Deps struct {
	Issuer       *authztoken.Issuer
	AuthHandlers *authzhttp.Handlers // /api/auth/*; nil in unit tests that skip auth routes
	RoleResolver authzhttp.RoleResolver
	OrgLookup    OrgLookup
	Asker        Asker
	Community    CommunityReader // GraphRAG community views (M3); nil disables those routes
	Ingester     Ingester
	KBRepo       *orgkb.Repo // used by kb CRUD handlers; nil in focused unit tests
	PerUserLimit int

	MaxUploadBytes      int64           // http.MaxBytesReader cap per document body
	KBStorageQuotaBytes int64           // per-kb cumulative content-byte quota (§16.3)
	DocStatus           DocStatusReader // reads document status for SSE; satisfied by *ingest.Service

	EvalRunner            EvalRunner    // eval run/list (M4); nil disables eval routes
	SessionReader         SessionReader // Q&A history reads (M4); nil disables session routes
	EvalRunsPerUserMinute int           // per-user eval-run budget (separate, smaller than the ask limiter)
}

// kbScopeFromRequest builds the ScopeFromRequest closure for kb-scoped routes
// (path `{id}`): kbID from the path, orgID via the OrgLookup. On a missing kb it
// returns ("",kbID) which resolves to RoleNone → 403, the safe default.
func kbScopeFromRequest(lookup OrgLookup) authzhttp.ScopeFromRequest {
	return func(r *http.Request) (string, string) {
		kbID := r.PathValue("id")
		orgID, err := lookup.OrgIDForKB(r.Context(), kbID)
		if err != nil {
			return "", kbID
		}
		return orgID, kbID
	}
}

// orgScopeFromRequest builds the ScopeFromRequest for ORG-scoped routes
// (path `{org}`): orgID from the path, scopeID="" so ResolveRole(...,"kb","")
// matches the org-level membership row (`scope_id IS NULL OR scope_id=$4`). An
// org_admin (rank 4) thus satisfies the kb admin/viewer minimum on these routes.
func orgScopeFromRequest(r *http.Request) (string, string) {
	return r.PathValue("org"), ""
}

// NewMux builds the kb ServeMux with the auth routes and the RBAC-guarded kb routes.
func NewMux(d Deps) *http.ServeMux {
	mux := http.NewServeMux()
	if d.AuthHandlers != nil {
		d.AuthHandlers.Mount(mux, "/api/auth")
	}
	guard := limits.New(d.PerUserLimit)
	evalGuard := limits.New(d.EvalRunsPerUserMinute)
	kbScope := kbScopeFromRequest(d.OrgLookup)

	// authOnly composes: Authenticate → per-user limit → handler. No scope role
	// check — used by POST /api/orgs (any authenticated user may create an org).
	authOnly := func(h http.HandlerFunc) http.Handler {
		var handler http.Handler = withUserLimit(guard, h)
		return authzhttp.Authenticate(d.Issuer)(handler)
	}
	// scoped composes: Authenticate → RequireScopeRole(min, scope) → per-user
	// limit → handler, for an arbitrary ScopeFromRequest.
	scoped := func(min authzrole.Role, scope authzhttp.ScopeFromRequest, h http.HandlerFunc) http.Handler {
		var handler http.Handler = withUserLimit(guard, h)
		handler = authzhttp.RequireScopeRole(d.RoleResolver, "kb", min, scope)(handler)
		return authzhttp.Authenticate(d.Issuer)(handler)
	}
	// chain is the kb-scoped (`{id}`) shorthand.
	chain := func(min authzrole.Role, h http.HandlerFunc) http.Handler {
		return scoped(min, kbScope, h)
	}

	// Org bootstrap + kb create/list (§16.2). Wired only when KBRepo is set.
	if d.KBRepo != nil {
		// POST /api/orgs — any authenticated user; creator becomes org_admin.
		mux.Handle("POST /api/orgs", authOnly(createOrgHandler(d.KBRepo)))
		// POST/GET /api/orgs/{org}/kbs — org-level RBAC (org_admin satisfies it).
		mux.Handle("POST /api/orgs/{org}/kbs", scoped(authzrole.RoleAdmin, orgScopeFromRequest, createKBHandler(d.KBRepo)))
		mux.Handle("GET /api/orgs/{org}/kbs", scoped(authzrole.RoleViewer, orgScopeFromRequest, listKBHandler(d.KBRepo)))
	}

	// Q&A — viewer+.
	mux.Handle("POST /api/kb/{id}/ask", chain(authzrole.RoleViewer, askHandler(d.Asker)))
	// GraphRAG Q&A (M3) — global map-reduce + drift; viewer+, namespace-only.
	mux.Handle("POST /api/kb/{id}/ask/global", chain(authzrole.RoleViewer, askGlobalHandler(d.Asker)))
	mux.Handle("POST /api/kb/{id}/ask/drift", chain(authzrole.RoleViewer, askDriftHandler(d.Asker)))
	// Community views (M3) — viewer+; wired only when a CommunityReader + KBRepo are set.
	if d.Community != nil && d.KBRepo != nil {
		mux.Handle("GET /api/kb/{id}/communities", chain(authzrole.RoleViewer, listCommunitiesHandler(d.KBRepo, d.Community)))
		mux.Handle("GET /api/kb/{id}/communities/{cid}", chain(authzrole.RoleViewer, communityReportHandler(d.KBRepo, d.Community)))
	}
	// Documents — upload requires editor+, read viewer+, delete editor+.
	if d.Ingester != nil && d.KBRepo != nil {
		mux.Handle("POST /api/kb/{id}/documents", chain(authzrole.RoleEditor, uploadHandler(d.KBRepo, d.Ingester, d.MaxUploadBytes, d.KBStorageQuotaBytes)))
		mux.Handle("GET /api/kb/{id}/documents", chain(authzrole.RoleViewer, listDocsHandler(d.KBRepo, d.Ingester)))
		mux.Handle("GET /api/kb/{id}/documents/{docId}", chain(authzrole.RoleViewer, getDocHandler(d.KBRepo, d.DocStatus)))
		mux.Handle("GET /api/kb/{id}/documents/{docId}/progress", chain(authzrole.RoleViewer, progressHandler(d.KBRepo, d.DocStatus)))
		mux.Handle("POST /api/kb/{id}/documents/{docId}/retry", chain(authzrole.RoleEditor, retryHandler(d.KBRepo, d.Ingester)))
		mux.Handle("DELETE /api/kb/{id}/documents/{docId}", chain(authzrole.RoleEditor, deleteDocHandler(d.KBRepo, d.Ingester)))
		// kb resource — delete is admin (cascade wired in deleteKBHandler).
		mux.Handle("GET /api/kb/{id}", chain(authzrole.RoleViewer, getKBHandler(d.KBRepo)))
		mux.Handle("DELETE /api/kb/{id}", chain(authzrole.RoleAdmin, deleteKBHandler(d.KBRepo, d.Ingester)))
	}
	// Eval (M4) — run is editor+, list is viewer+. Wired only when an EvalRunner + KBRepo are set.
	if d.EvalRunner != nil && d.KBRepo != nil {
		mux.Handle("POST /api/kb/{id}/eval/run", chain(authzrole.RoleEditor, evalRunHandler(d.KBRepo, d.EvalRunner, evalGuard)))
		mux.Handle("GET /api/kb/{id}/eval/runs", chain(authzrole.RoleViewer, listRunsHandler(d.KBRepo, d.EvalRunner)))
	}
	// Sessions (M4) — viewer+. Wired only when a SessionReader + KBRepo are set.
	if d.SessionReader != nil && d.KBRepo != nil {
		mux.Handle("GET /api/kb/{id}/sessions", chain(authzrole.RoleViewer, listSessionsHandler(d.KBRepo, d.SessionReader)))
		mux.Handle("GET /api/kb/{id}/sessions/{sid}", chain(authzrole.RoleViewer, sessionTranscriptHandler(d.KBRepo, d.SessionReader)))
	}
	return mux
}

// withUserLimit enforces the per-user request budget after auth (so UserID is set).
func withUserLimit(g *limits.Guard, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := authzhttp.UserID(r.Context())
		if !g.Allow(uid) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func askHandler(asker Asker) http.HandlerFunc {
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
		out, err := asker.Ask(r.Context(), retrieval.AskInput{
			Namespace: "kb_" + r.PathValue("id"), // namespace = "kb_"+id (orgkb.Create convention)
			KBID:      r.PathValue("id"),
			UserID:    authzhttp.UserID(r.Context()),
			SessionID: req.SessionID,
			Question:  req.Q,
			Mode:      req.Mode,
			TopK:      req.TopK,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// askGlobalHandler runs GraphRAG global map-reduce (viewer+, §7). Namespace =
// "kb_"+id (same convention as askHandler); namespace-only isolation (§8).
func askGlobalHandler(asker Asker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Q              string `json:"q"`
			MaxCommunities int    `json:"maxCommunities"`
			SessionID      string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		out, err := asker.AskGlobal(r.Context(), retrieval.GlobalInput{
			Namespace:      "kb_" + r.PathValue("id"),
			KBID:           r.PathValue("id"),
			UserID:         authzhttp.UserID(r.Context()),
			SessionID:      req.SessionID,
			Question:       req.Q,
			MaxCommunities: req.MaxCommunities,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// askDriftHandler runs GraphRAG drift search (viewer+, §7). Namespace-only (§8).
func askDriftHandler(asker Asker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Q              string `json:"q"`
			MaxCommunities int    `json:"maxCommunities"`
			Rounds         int    `json:"rounds"`
			TopK           int    `json:"topK"`
			SessionID      string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		out, err := asker.AskDrift(r.Context(), retrieval.DriftInput{
			Namespace:      "kb_" + r.PathValue("id"),
			KBID:           r.PathValue("id"),
			UserID:         authzhttp.UserID(r.Context()),
			SessionID:      req.SessionID,
			Question:       req.Q,
			MaxCommunities: req.MaxCommunities,
			Rounds:         req.Rounds,
			TopK:           req.TopK,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
