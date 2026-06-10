package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-authz/password"
	authzstore "github.com/costa92/llm-agent-authz/store"
	"github.com/costa92/llm-agent-contract/llm"

	"github.com/costa92/llm-agent-kb/internal/config"
)

// TestEndToEndLoginCreateKBUploadAskDelete drives the full M2 async success path
// over the real HTTP surface on live pgvector:
// seed user → login → POST /api/orgs → POST /api/orgs/{org}/kbs →
// upload paste (202) → poll documents until ready (worker drains the queue) →
// ask (hybrid) → assert ≥1 citation → delete document → assert chunks gone.
// Providers are scripted (no ollama/openai); only PG is required.
func TestEndToEndLoginCreateKBUploadAskDelete(t *testing.T) {
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the e2e smoke test")
	}
	ctx := context.Background()
	cleanDB(t, ctx, dsn)

	providerOverride = func(config.Config) (llm.ChatModel, llm.Embedder, error) {
		return llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "scripted answer"})),
			llm.NewScriptedLLM(llm.WithEmbedDimensions(8)), nil
	}
	t.Cleanup(func() { providerOverride = nil })

	cfg, err := config.LoadFromLookup(func(k string) (string, bool) {
		switch k {
		case "PG_URL":
			return dsn, true
		case "EMBEDDING_DIM":
			return "8", true
		case "JWT_SECRET":
			return "test-secret", true
		case "GRAPH_ENABLED":
			// This e2e proves the M2 async-ingest + ask + delete path with a
			// single scripted response; graph extraction would consume that
			// response during ingest (cursor exhausts before ask). GraphRAG is
			// covered by TestGraphRAGEndToEnd, which over-provisions the script.
			return "false", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup, err := build(ctx, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Seed a user directly via the authz store (no signup endpoint in M1).
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	hash, err := password.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authzstore.New(pool).CreateUser(ctx, "e2e@x.com", hash); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	client := srv.Client()
	// do issues a JSON request with optional bearer and returns status + decoded body.
	do := func(method, path, bearer, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m == nil {
			m = map[string]any{"_raw": string(raw)}
		}
		return resp.StatusCode, m
	}

	// 1. Login → access_token.
	code, body := do("POST", "/api/auth/login", "", `{"Email":"e2e@x.com","Password":"pw"}`)
	if code != http.StatusOK {
		t.Fatalf("login code=%d body=%v want 200", code, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatalf("login returned no access_token: %v", body)
	}

	// 2. POST /api/orgs → creator becomes org_admin.
	code, body = do("POST", "/api/orgs", token, `{"name":"Acme"}`)
	if code != http.StatusOK {
		t.Fatalf("create org code=%d body=%v want 200", code, body)
	}
	orgID, _ := body["id"].(string)

	// 3. POST /api/orgs/{org}/kbs (org_admin) → kb id.
	code, body = do("POST", "/api/orgs/"+orgID+"/kbs", token, `{"name":"Docs","embeddingDim":8}`)
	if code != http.StatusOK {
		t.Fatalf("create kb code=%d body=%v want 200", code, body)
	}
	kbID, _ := body["id"].(string)
	if kbID == "" {
		t.Fatalf("create kb returned no id: %v", body)
	}

	// 4. POST /api/kb/{id}/documents (paste) → 202 + documentId.
	code, body = do("POST", "/api/kb/"+kbID+"/documents", token,
		`{"title":"Doc","sourceType":"paste","content":"the quick brown fox jumps over the lazy dog repeatedly"}`)
	if code != http.StatusAccepted {
		t.Fatalf("upload code=%d body=%v want 202", code, body)
	}
	docID, _ := body["documentId"].(string)
	if docID == "" {
		t.Fatalf("upload returned no documentId: %v", body)
	}

	// 4b. Poll GET /api/kb/{id}/documents until the doc is ready (worker drains async).
	// The existing `do` closure already decodes JSON, so it handles the list
	// envelope {items, next_cursor} directly — no separate helper needed.
	ready := false
	for i := 0; i < 50; i++ {
		code, listBody := do("GET", "/api/kb/"+kbID+"/documents", token, "")
		if code == http.StatusOK && hasReadyDoc(listBody, docID) {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("document %s never reached ready", docID)
	}

	// 5. POST /api/kb/{id}/ask (hybrid) → answer + ≥1 citation.
	code, body = do("POST", "/api/kb/"+kbID+"/ask", token, `{"q":"fox","mode":"hybrid","topK":5}`)
	if code != http.StatusOK {
		t.Fatalf("ask code=%d body=%v want 200", code, body)
	}
	cites, _ := body["citations"].([]any)
	if len(cites) == 0 {
		t.Fatalf("ask returned 0 citations: %v", body)
	}

	// 6. DELETE /api/kb/{id}/documents/{docId} → 204, chunks gone.
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/kb/"+kbID+"/documents/"+docID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete doc: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete doc code=%d want 204", resp.StatusCode)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE namespace = $1`, "kb_"+kbID).Scan(&remaining); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("chunks remain after document delete: %d", remaining)
	}
}

// TestGraphRAGEndToEnd drives the M3 GraphRAG HTTP surface end-to-end on live
// pgvector: login → org → kb → upload paste → poll ready (worker ingests +
// extracts entities + builds communities + prewarms reports) → GET /communities
// → POST /ask/global → POST /ask/drift. It proves the HTTP + auth + routing +
// namespace wiring; engine correctness (real communities, ReduceCalls==1) is
// proven by the ragsvc gated test (Task 4). The scripted model is generously
// over-provisioned so the production LLMEntityExtractor's per-chunk Generate
// cursor never exhausts for the tiny corpus; community count is tolerated as
// degenerate (assert only the 200s + ready + non-empty global answer).
func TestGraphRAGEndToEnd(t *testing.T) {
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the GraphRAG e2e")
	}
	ctx := context.Background()
	cleanDB(t, ctx, dsn)

	// Extraction pipe-lines first (consumed during Import), then summary +
	// map(Score: prefix)/reduce + drift text, then spare responses so the
	// single Generate cursor never exhausts for the tiny corpus.
	model := llm.NewScriptedLLM(llm.WithResponses(
		llm.TextResponse("ENTITY | Acme | org | a company\nENTITY | Paris | city | a place\nRELATION | Acme | Paris | located_in | hq"),
		llm.TextResponse("Theme: Acme\nReport about Acme and Paris."),
		llm.TextResponse("Score: 80\nmap result"),
		llm.TextResponse("GLOBAL: Acme is in Paris."),
		llm.TextResponse("primer result"),
		llm.TextResponse("DRIFT: details about Acme."),
		llm.TextResponse("spare1"), llm.TextResponse("spare2"),
		llm.TextResponse("spare3"), llm.TextResponse("spare4"),
		llm.TextResponse("spare5"), llm.TextResponse("spare6"),
	))
	providerOverride = func(config.Config) (llm.ChatModel, llm.Embedder, error) {
		return model, llm.NewScriptedLLM(llm.WithEmbedDimensions(8)), nil
	}
	t.Cleanup(func() { providerOverride = nil })

	cfg, err := config.LoadFromLookup(func(k string) (string, bool) {
		switch k {
		case "PG_URL":
			return dsn, true
		case "EMBEDDING_DIM":
			return "8", true
		case "JWT_SECRET":
			return "test-secret", true
		case "GRAPH_ENABLED":
			return "true", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup, err := build(ctx, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	hash, err := password.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authzstore.New(pool).CreateUser(ctx, "graph@x.com", hash); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	client := srv.Client()
	do := func(method, path, bearer, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m == nil {
			m = map[string]any{"_raw": string(raw)}
		}
		return resp.StatusCode, m
	}

	code, body := do("POST", "/api/auth/login", "", `{"Email":"graph@x.com","Password":"pw"}`)
	if code != http.StatusOK {
		t.Fatalf("login code=%d body=%v want 200", code, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatalf("login returned no access_token: %v", body)
	}

	code, body = do("POST", "/api/orgs", token, `{"name":"Acme"}`)
	if code != http.StatusOK {
		t.Fatalf("create org code=%d body=%v want 200", code, body)
	}
	orgID, _ := body["id"].(string)

	code, body = do("POST", "/api/orgs/"+orgID+"/kbs", token, `{"name":"Docs","embeddingDim":8}`)
	if code != http.StatusOK {
		t.Fatalf("create kb code=%d body=%v want 200", code, body)
	}
	kbID, _ := body["id"].(string)
	if kbID == "" {
		t.Fatalf("create kb returned no id: %v", body)
	}

	code, body = do("POST", "/api/kb/"+kbID+"/documents", token,
		`{"title":"Doc","sourceType":"paste","content":"Acme is a company headquartered in Paris. Acme operates in Paris."}`)
	if code != http.StatusAccepted {
		t.Fatalf("upload code=%d body=%v want 202", code, body)
	}
	docID, _ := body["documentId"].(string)
	if docID == "" {
		t.Fatalf("upload returned no documentId: %v", body)
	}

	// Poll the single-doc endpoint (GET /documents/{docId}, M3 Task 8) until ready.
	ready := false
	for i := 0; i < 50; i++ {
		code, docBody := do("GET", "/api/kb/"+kbID+"/documents/"+docID, token, "")
		if code == http.StatusOK && docBody["status"] == "ready" {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("document %s never reached ready", docID)
	}

	// GET /communities → 200 (tolerate degenerate count for a tiny corpus).
	if code, body = do("GET", "/api/kb/"+kbID+"/communities", token, ""); code != http.StatusOK {
		t.Fatalf("list communities code=%d body=%v want 200", code, body)
	}

	// POST /ask/global → 200, non-empty answer (canned "no community info" still
	// counts as non-empty if extraction produced no communities).
	code, body = do("POST", "/api/kb/"+kbID+"/ask/global", token, `{"q":"where is Acme?","maxCommunities":8}`)
	if code != http.StatusOK {
		t.Fatalf("ask/global code=%d body=%v want 200", code, body)
	}
	if ans, _ := body["answer"].(string); ans == "" {
		t.Fatalf("ask/global returned empty answer: %v", body)
	}

	// POST /ask/drift → 200.
	if code, body = do("POST", "/api/kb/"+kbID+"/ask/drift", token, `{"q":"tell me about Acme","rounds":1,"topK":3}`); code != http.StatusOK {
		t.Fatalf("ask/drift code=%d body=%v want 200", code, body)
	}
}

// TestEvalAndSessionsEndToEnd drives the M4 quality surface on live pgvector:
// login → org → kb → upload paste → poll ready → ask (hybrid) → GET /sessions
// (assert the ask created a session + persisted the pair) → GET /sessions/{sid}
// (assert 2 messages) → POST /eval/run (retrieval, tiny inline JSONL) → GET
// /eval/runs (assert ≥1 run) → POST /eval/run (drift) → GET /eval/runs (assert
// the drift run stored). Engine correctness is rag's own concern; this proves
// the HTTP + auth + persistence wiring. The scripted model is over-provisioned
// so the judge/ask cursors never exhaust.
func TestEvalAndSessionsEndToEnd(t *testing.T) {
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the M4 eval/sessions e2e")
	}
	ctx := context.Background()
	cleanDB(t, ctx, dsn)

	// One scripted Generate cursor backs ask answers + the LLM judge JSON.
	// Over-provision generously (ingest with GRAPH_ENABLED=false consumes none;
	// ask + each eval example + each drift example each draw one).
	responses := []llm.Response{}
	for i := 0; i < 40; i++ {
		responses = append(responses, llm.Response{Text: `{"groundedness":0.9,"answer_relevance":0.9}`})
	}
	model := llm.NewScriptedLLM(llm.WithResponses(responses...))
	providerOverride = func(config.Config) (llm.ChatModel, llm.Embedder, error) {
		return model, llm.NewScriptedLLM(llm.WithEmbedDimensions(8)), nil
	}
	t.Cleanup(func() { providerOverride = nil })

	cfg, err := config.LoadFromLookup(func(k string) (string, bool) {
		switch k {
		case "PG_URL":
			return dsn, true
		case "EMBEDDING_DIM":
			return "8", true
		case "JWT_SECRET":
			return "test-secret", true
		case "GRAPH_ENABLED":
			return "false", true // eval here is retrieval+drift over the local path
		case "MAX_REQUESTS_PER_USER_PER_MINUTE":
			// This flow makes many requests (doc-readiness polling + sessions
			// reads + 3 eval runs + 2 list calls) within one minute; the default
			// 30/min global guard would 429 it. The e2e proves functionality, not
			// rate limiting, so lift the cap.
			return "1000", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup, err := build(ctx, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	hash, err := password.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authzstore.New(pool).CreateUser(ctx, "m4@x.com", hash); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	client := srv.Client()
	do := func(method, path, bearer, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m == nil {
			m = map[string]any{"_raw": string(raw)}
		}
		return resp.StatusCode, m
	}

	code, body := do("POST", "/api/auth/login", "", `{"Email":"m4@x.com","Password":"pw"}`)
	if code != http.StatusOK {
		t.Fatalf("login code=%d body=%v", code, body)
	}
	token, _ := body["access_token"].(string)

	code, body = do("POST", "/api/orgs", token, `{"name":"Acme"}`)
	if code != http.StatusOK {
		t.Fatalf("create org code=%d body=%v", code, body)
	}
	orgID, _ := body["id"].(string)

	code, body = do("POST", "/api/orgs/"+orgID+"/kbs", token, `{"name":"Docs","embeddingDim":8}`)
	if code != http.StatusOK {
		t.Fatalf("create kb code=%d body=%v", code, body)
	}
	kbID, _ := body["id"].(string)

	code, body = do("POST", "/api/kb/"+kbID+"/documents", token,
		`{"title":"Doc","sourceType":"paste","content":"the quick brown fox jumps over the lazy dog repeatedly"}`)
	if code != http.StatusAccepted {
		t.Fatalf("upload code=%d body=%v", code, body)
	}
	docID, _ := body["documentId"].(string)
	ready := false
	for i := 0; i < 50; i++ {
		code, listBody := do("GET", "/api/kb/"+kbID+"/documents", token, "")
		if code == http.StatusOK && hasReadyDoc(listBody, docID) {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("document never ready")
	}

	// Ask (hybrid) → assert a session id comes back.
	code, body = do("POST", "/api/kb/"+kbID+"/ask", token, `{"q":"fox","mode":"hybrid","topK":5}`)
	if code != http.StatusOK {
		t.Fatalf("ask code=%d body=%v", code, body)
	}
	sid, _ := body["sessionId"].(string)
	if sid == "" {
		t.Fatalf("ask returned no sessionId: %v", body)
	}

	// GET /sessions → assert the session is listed.
	code, body = do("GET", "/api/kb/"+kbID+"/sessions", token, "")
	if code != http.StatusOK {
		t.Fatalf("list sessions code=%d body=%v", code, body)
	}
	sessItems, _ := body["items"].([]any)
	if len(sessItems) == 0 {
		t.Fatalf("no sessions persisted: %v", body)
	}

	// GET /sessions/{sid} → assert 2 messages (user + assistant).
	code, body = do("GET", "/api/kb/"+kbID+"/sessions/"+sid, token, "")
	if code != http.StatusOK {
		t.Fatalf("transcript code=%d body=%v", code, body)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("transcript msgs = %d, want 2: %v", len(msgs), body)
	}

	// POST /eval/run (retrieval) → 200 + runId. Tiny inline JSONL dataset.
	evalBody := `{"kind":"retrieval","dataset":"{\"query\":\"fox\",\"gold_doc_ids\":[\"` + docID + `\"],\"top_k\":5}"}`
	code, body = do("POST", "/api/kb/"+kbID+"/eval/run", token, evalBody)
	if code != http.StatusOK {
		t.Fatalf("eval/run retrieval code=%d body=%v", code, body)
	}
	if body["runId"] == nil || body["runId"] == "" {
		t.Fatalf("eval/run returned no runId: %v", body)
	}

	// GET /eval/runs → assert ≥1 stored run.
	code, body = do("GET", "/api/kb/"+kbID+"/eval/runs", token, "")
	if code != http.StatusOK {
		t.Fatalf("eval/runs code=%d body=%v", code, body)
	}
	runItems, _ := body["items"].([]any)
	if len(runItems) == 0 {
		t.Fatalf("no eval runs stored: %v", body)
	}

	// POST /eval/run (drift) twice so the second has a baseline; assert both 200.
	driftBody := `{"kind":"drift","dataset":"{\"query\":\"fox\",\"top_k\":5}"}`
	if code, body = do("POST", "/api/kb/"+kbID+"/eval/run", token, driftBody); code != http.StatusOK {
		t.Fatalf("eval/run drift#1 code=%d body=%v", code, body)
	}
	if code, body = do("POST", "/api/kb/"+kbID+"/eval/run", token, driftBody); code != http.StatusOK {
		t.Fatalf("eval/run drift#2 code=%d body=%v", code, body)
	}

	// GET /eval/runs → assert the drift run(s) are stored (≥3 total now).
	code, body = do("GET", "/api/kb/"+kbID+"/eval/runs?limit=100", token, "")
	if code != http.StatusOK {
		t.Fatalf("eval/runs#2 code=%d body=%v", code, body)
	}
	runItems, _ = body["items"].([]any)
	if len(runItems) < 3 {
		t.Fatalf("eval runs = %d, want ≥3 (1 retrieval + 2 drift): %v", len(runItems), body)
	}
	// Assert a drift run carries the drift envelope.
	foundDrift := false
	for _, it := range runItems {
		m, _ := it.(map[string]any)
		if m["kind"] == "drift" && m["drift"] != nil {
			foundDrift = true
		}
	}
	if !foundDrift {
		t.Fatalf("no drift run with a drift payload: %v", runItems)
	}
}

// TestStreamingAskEndToEnd drives POST /api/kb/{id}/ask/stream over live
// pgvector: login → org → kb → upload paste → poll ready → open the SSE stream
// → assert ≥1 token frame + a terminal done frame with ≥1 citation + sessionId,
// then assert the q/a pair persisted (qa_session row exists). Providers are
// scripted: ScriptedLLM.Stream emits one EventTextDelta then EventDone, so the
// answer arrives as a single token frame. Gated on LLM_AGENT_KB_PG_URL.
func TestStreamingAskEndToEnd(t *testing.T) {
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the streaming e2e")
	}
	ctx := context.Background()
	cleanDB(t, ctx, dsn)

	providerOverride = func(config.Config) (llm.ChatModel, llm.Embedder, error) {
		return llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "scripted streamed answer"})),
			llm.NewScriptedLLM(llm.WithEmbedDimensions(8)), nil
	}
	t.Cleanup(func() { providerOverride = nil })

	cfg, err := config.LoadFromLookup(func(k string) (string, bool) {
		switch k {
		case "PG_URL":
			return dsn, true
		case "EMBEDDING_DIM":
			return "8", true
		case "JWT_SECRET":
			return "test-secret", true
		case "GRAPH_ENABLED":
			return "false", true
		case "MAX_REQUESTS_PER_USER_PER_MINUTE":
			// The doc-readiness poll loop + stream + sessions read can exceed the
			// default 30/min global guard within one minute; the e2e proves
			// functionality, not rate limiting, so lift the cap (mirrors the M4
			// eval e2e).
			return "1000", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup, err := build(ctx, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	hash, err := password.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authzstore.New(pool).CreateUser(ctx, "stream@x.com", hash); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	client := srv.Client()
	do := func(method, path, bearer, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m == nil {
			m = map[string]any{"_raw": string(raw)}
		}
		return resp.StatusCode, m
	}

	code, body := do("POST", "/api/auth/login", "", `{"Email":"stream@x.com","Password":"pw"}`)
	if code != http.StatusOK {
		t.Fatalf("login code=%d body=%v want 200", code, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatalf("login returned no access_token: %v", body)
	}

	code, body = do("POST", "/api/orgs", token, `{"name":"Acme"}`)
	if code != http.StatusOK {
		t.Fatalf("create org code=%d body=%v want 200", code, body)
	}
	orgID, _ := body["id"].(string)

	code, body = do("POST", "/api/orgs/"+orgID+"/kbs", token, `{"name":"Docs","embeddingDim":8}`)
	if code != http.StatusOK {
		t.Fatalf("create kb code=%d body=%v want 200", code, body)
	}
	kbID, _ := body["id"].(string)
	if kbID == "" {
		t.Fatalf("create kb returned no id: %v", body)
	}

	code, body = do("POST", "/api/kb/"+kbID+"/documents", token,
		`{"title":"Doc","sourceType":"paste","content":"the quick brown fox jumps over the lazy dog repeatedly"}`)
	if code != http.StatusAccepted {
		t.Fatalf("upload code=%d body=%v want 202", code, body)
	}
	docID, _ := body["documentId"].(string)
	if docID == "" {
		t.Fatalf("upload returned no documentId: %v", body)
	}

	ready := false
	for i := 0; i < 50; i++ {
		code, listBody := do("GET", "/api/kb/"+kbID+"/documents", token, "")
		if code == http.StatusOK && hasReadyDoc(listBody, docID) {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("document %s never reached ready", docID)
	}

	// Open the SSE stream and parse frames.
	streamReq, _ := http.NewRequest("POST", srv.URL+"/api/kb/"+kbID+"/ask/stream",
		strings.NewReader(`{"q":"fox","mode":"hybrid","topK":5}`))
	streamReq.Header.Set("Content-Type", "application/json")
	streamReq.Header.Set("Authorization", "Bearer "+token)
	streamResp, err := client.Do(streamReq)
	if err != nil {
		t.Fatalf("ask/stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("ask/stream code=%d want 200", streamResp.StatusCode)
	}
	if ct := streamResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("ask/stream Content-Type=%q want text/event-stream", ct)
	}

	var tokenCount int
	var doneData map[string]any
	var curEvent string
	sc := bufio.NewScanner(streamResp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			curEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			switch curEvent {
			case "token":
				tokenCount++
			case "done":
				_ = json.Unmarshal([]byte(data), &doneData)
			case "error":
				t.Fatalf("stream emitted error frame: %s", data)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan SSE: %v", err)
	}
	if tokenCount == 0 {
		t.Fatal("expected at least one token frame")
	}
	if doneData == nil {
		t.Fatal("expected a terminal done frame")
	}
	cites, _ := doneData["citations"].([]any)
	if len(cites) == 0 {
		t.Fatalf("done frame returned 0 citations: %v", doneData)
	}
	sid, _ := doneData["sessionId"].(string)
	if sid == "" {
		t.Fatalf("done frame missing sessionId: %v", doneData)
	}

	// Persistence: the streamed q/a pair created a session row for this kb.
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_session WHERE kb_id = $1`, kbID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions == 0 {
		t.Fatal("streamed ask did not persist a qa_session row")
	}
}

// hasReadyDoc reports whether the document list envelope contains docID at status "ready".
func hasReadyDoc(listBody map[string]any, docID string) bool {
	items, _ := listBody["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["id"] == docID && m["status"] == "ready" {
			return true
		}
	}
	return false
}

// cleanDB drops business + authz + rag tables for a deterministic run.
func cleanDB(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("cleanDB pool: %v", err)
	}
	defer pool.Close()
	for _, tbl := range []string{
		"ingest_job",
		"qa_message", "qa_session", "eval_run",
		"document", "knowledge_base",
		"chunks", "chunks_entities", "chunks_relations", "chunks_communities", "chunks_community_reports",
		"auth_membership", "auth_session", "auth_user", "auth_org",
	} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
}
