package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-authz/password"
	authzrole "github.com/costa92/llm-agent-authz/role"
	authzstore "github.com/costa92/llm-agent-authz/store"
	authztoken "github.com/costa92/llm-agent-authz/token"

	"github.com/costa92/llm-agent-kb/internal/orgkb"
	"github.com/costa92/llm-agent-kb/internal/retrieval"
)

// fakeResolver implements authzhttp.RoleResolver semantics for tests.
type fakeResolver struct{ role authzrole.Role }

func (f fakeResolver) ResolveRole(_ context.Context, _, _, _, _ string) (authzrole.Role, error) {
	return f.role, nil
}

// fakeOrgLookup returns a fixed org for any kb.
type fakeOrgLookup struct{}

func (fakeOrgLookup) OrgIDForKB(_ context.Context, _ string) (string, error) { return "org-1", nil }

// fakeAsk implements the Asker the ask handler needs.
type fakeAsk struct{ out retrieval.AskOutput }

func (f fakeAsk) Ask(context.Context, retrieval.AskInput) (retrieval.AskOutput, error) {
	return f.out, nil
}

func bearer(t *testing.T, iss *authztoken.Issuer, uid string) string {
	t.Helper()
	tok, err := iss.Issue(uid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + tok
}

func TestAskRequiresViewer(t *testing.T) {
	iss := authztoken.NewIssuer([]byte("s"), time.Minute)
	deps := Deps{
		Issuer:       iss,
		RoleResolver: fakeResolver{role: authzrole.RoleNone}, // no membership
		OrgLookup:    fakeOrgLookup{},
		Asker:        fakeAsk{out: retrieval.AskOutput{Answer: "x"}},
		PerUserLimit: 0,
	}
	mux := NewMux(deps)
	req := httptest.NewRequest("POST", "/api/kb/kb-1/ask", strings.NewReader(`{"q":"hi","mode":"vector"}`))
	req.Header.Set("Authorization", bearer(t, iss, "u1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-membership ask code=%d want 403", rec.Code)
	}
}

func TestAskAllowedForViewer(t *testing.T) {
	iss := authztoken.NewIssuer([]byte("s"), time.Minute)
	deps := Deps{
		Issuer:       iss,
		RoleResolver: fakeResolver{role: authzrole.RoleViewer},
		OrgLookup:    fakeOrgLookup{},
		Asker:        fakeAsk{out: retrieval.AskOutput{Answer: "the answer", Citations: []retrieval.Citation{}}},
	}
	mux := NewMux(deps)
	req := httptest.NewRequest("POST", "/api/kb/kb-1/ask", strings.NewReader(`{"q":"hi","mode":"vector"}`))
	req.Header.Set("Authorization", bearer(t, iss, "u1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer ask code=%d body=%s want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "the answer") {
		t.Fatalf("body=%s", rec.Body)
	}
}

func TestAskUnauthenticated401(t *testing.T) {
	iss := authztoken.NewIssuer([]byte("s"), time.Minute)
	mux := NewMux(Deps{Issuer: iss, RoleResolver: fakeResolver{role: authzrole.RoleViewer}, OrgLookup: fakeOrgLookup{}, Asker: fakeAsk{}})
	req := httptest.NewRequest("POST", "/api/kb/kb-1/ask", strings.NewReader(`{"q":"hi","mode":"vector"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token ask code=%d want 401", rec.Code)
	}
}

func TestRateLimitExceeded429(t *testing.T) {
	iss := authztoken.NewIssuer([]byte("s"), time.Minute)
	deps := Deps{
		Issuer:       iss,
		RoleResolver: fakeResolver{role: authzrole.RoleViewer},
		OrgLookup:    fakeOrgLookup{},
		Asker:        fakeAsk{out: retrieval.AskOutput{Answer: "x"}},
		PerUserLimit: 1,
	}
	mux := NewMux(deps)
	do := func() int {
		req := httptest.NewRequest("POST", "/api/kb/kb-1/ask", strings.NewReader(`{"q":"hi","mode":"vector"}`))
		req.Header.Set("Authorization", bearer(t, iss, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if do() != http.StatusOK {
		t.Fatal("first request should pass")
	}
	if do() != http.StatusTooManyRequests {
		t.Fatal("second request should be rate-limited (429)")
	}
}

// --- Org/kb create+list endpoints (H1), gated on a live DB ---
// These routes use the concrete *orgkb.Repo (CreateOrg/Create/ListByOrg) and a
// real authz RoleResolver, so they need a DB (no pgvector required — no rag
// ops). Gated on LLM_AGENT_KB_PG_URL.

func openOrgEndpointDeps(t *testing.T, ctx context.Context) (Deps, *authzstore.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set %s to run org-endpoint tests", "LLM_AGENT_KB_PG_URL")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, tbl := range []string{"document", "knowledge_base", "auth_membership", "auth_session", "auth_user", "auth_org"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	az := authzstore.New(pool)
	if err := az.Migrate(ctx); err != nil {
		t.Fatalf("authz migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS knowledge_base (
		id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL,
		namespace TEXT NOT NULL UNIQUE, embedding_model TEXT NOT NULL DEFAULT '',
		embedding_dim INT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create knowledge_base: %v", err)
	}
	repo := orgkb.New(pool, az)
	iss := authztoken.NewIssuer([]byte("s"), time.Minute)
	return Deps{
		Issuer:       iss,
		RoleResolver: az, // real resolver: org-level org_admin matches kb scope
		OrgLookup:    repo,
		KBRepo:       repo,
	}, az, pool
}

func TestOrgEndpointsBootstrapAndRBAC(t *testing.T) {
	ctx := context.Background()
	deps, az, pool := openOrgEndpointDeps(t, ctx)
	mux := NewMux(deps)

	// Seed two users directly via the authz store (no signup endpoint in M1).
	hash, _ := password.Hash("pw")
	bossID, err := az.CreateUser(ctx, "boss@x.com", hash)
	if err != nil {
		t.Fatal(err)
	}
	outsiderID, _ := az.CreateUser(ctx, "outsider@x.com", hash)

	post := func(uid, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", bearer(t, deps.Issuer, uid))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 1. POST /api/orgs — any authenticated user; creator becomes org_admin.
	rec := post(bossID, "/api/orgs", `{"name":"Acme"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/orgs code=%d body=%s want 200", rec.Code, rec.Body)
	}
	var org struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &org); err != nil || org.ID == "" {
		t.Fatalf("create org body=%s err=%v", rec.Body, err)
	}
	if got, _ := az.ResolveRole(ctx, bossID, org.ID, "kb", "any"); got != authzrole.RoleOrgAdmin {
		t.Fatalf("creator role=%q want org_admin", got)
	}

	// 2. POST /api/orgs/{org}/kbs — 403 for a non-admin outsider.
	if rec := post(outsiderID, "/api/orgs/"+org.ID+"/kbs", `{"name":"Docs","embeddingDim":8}`); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider create-kb code=%d want 403", rec.Code)
	}
	// 200 for the org_admin; writes creator kb-admin.
	rec = post(bossID, "/api/orgs/"+org.ID+"/kbs", `{"name":"Docs","embeddingDim":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("org_admin create-kb code=%d body=%s want 200", rec.Code, rec.Body)
	}
	var kb struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &kb)
	// Create writes the creator's kb-scope admin row. ResolveRole would return
	// org_admin here (org-level row outranks it in the merge), so assert the
	// kb-scope row exists directly.
	var kbScopeRole string
	if err := pool.QueryRow(ctx,
		`SELECT role FROM auth_membership WHERE user_id=$1 AND org_id=$2 AND scope_kind='kb' AND scope_id=$3`,
		bossID, org.ID, kb.ID).Scan(&kbScopeRole); err != nil {
		t.Fatalf("kb-scope membership row not found: %v", err)
	}
	if kbScopeRole != string(authzrole.RoleAdmin) {
		t.Fatalf("kb-scope role=%q want admin", kbScopeRole)
	}

	// 3. GET /api/orgs/{org}/kbs — RBAC-gated; org_admin (viewer+) sees the kb.
	get := func(uid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/orgs/"+org.ID+"/kbs", nil)
		req.Header.Set("Authorization", bearer(t, deps.Issuer, uid))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(outsiderID); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider list code=%d want 403", rec.Code)
	}
	rec = get(bossID)
	if rec.Code != http.StatusOK {
		t.Fatalf("org_admin list code=%d body=%s want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), kb.ID) || !strings.Contains(rec.Body.String(), "next_cursor") {
		t.Fatalf("list body=%s want kb id + next_cursor envelope", rec.Body)
	}
}
