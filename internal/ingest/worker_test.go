package ingest

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-contract/llm"
	ragpostgres "github.com/costa92/llm-agent-rag/postgres"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

// liveEnvVar is declared in delete_test.go (same package).

// freshWorkerDB sets up its OWN database (M1 lesson: gated tests are not
// co-runnable on a shared DB). It drops everything it owns, migrates, and
// returns a pool + a ragsvc wired to a scripted embedder.
func freshWorkerDB(t *testing.T, ctx context.Context) (*pgxpool.Pool, *ragsvc.Service, *Service, *clock) {
	t.Helper()
	dsn := os.Getenv(liveEnvVar)
	if dsn == "" {
		t.Skipf("set %s (pgvector) to run worker tests", liveEnvVar)
	}
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return ragpostgres.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, tbl := range []string{"ingest_job", "document", "knowledge_base", "chunks", "chunks_entities", "chunks_relations", "chunks_communities", "chunks_community_reports"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	chunkStore, err := ragpostgres.New(pool, ragpostgres.Config{Dimension: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := chunkStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range businessMigrationsForTest {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("migrate %q: %v", stmt, err)
		}
	}
	// seed a kb row (FK target) — namespace ns1.
	if _, err := pool.Exec(ctx, `INSERT INTO knowledge_base (id, org_id, name, namespace) VALUES ('kb1','o1','KB','ns1')`); err != nil {
		t.Fatal(err)
	}
	model := llm.NewScriptedLLM(llm.WithResponses(llm.Response{Text: "ok", Usage: llm.Usage{TotalTokens: 1}}))
	embedder := llm.NewScriptedLLM(llm.WithEmbedDimensions(8))
	rag := ragsvc.New(ragsvc.Deps{Model: model, Embedder: embedder, RagStore: chunkStore, ChunkStore: chunkStore})
	clk := &clock{now: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)}
	svc := New(pool, rag)
	return pool, rag, svc, clk
}

func docStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, docID string) (string, string) {
	t.Helper()
	var status, phase string
	if err := pool.QueryRow(ctx, `SELECT status, phase FROM document WHERE id=$1`, docID).Scan(&status, &phase); err != nil {
		t.Fatalf("doc status: %v", err)
	}
	return status, phase
}

func jobState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, docID string) (string, int) {
	t.Helper()
	var state string
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT state, attempts FROM ingest_job WHERE document_id=$1`, docID).Scan(&state, &attempts); err != nil {
		t.Fatalf("job state: %v", err)
	}
	return state, attempts
}

func TestEnqueueWritesDocumentAndJob(t *testing.T) {
	ctx := context.Background()
	pool, _, svc, _ := freshWorkerDB(t, ctx)
	docID, err := svc.Enqueue(ctx, IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypePaste, Raw: []byte("the quick brown fox")})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if status, _ := docStatus(t, ctx, pool, docID); status != "pending" {
		t.Fatalf("status=%q want pending", status)
	}
	if state, _ := jobState(t, ctx, pool, docID); state != "pending" {
		t.Fatalf("job state=%q want pending", state)
	}
}

func TestWorkerRunOnceProcessesToReady(t *testing.T) {
	ctx := context.Background()
	pool, rag, svc, clk := freshWorkerDB(t, ctx)
	docID, _ := svc.Enqueue(ctx, IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypePaste, Raw: []byte("the quick brown fox jumps over the lazy dog repeatedly")})

	w := NewWorker(WorkerConfig{Pool: pool, Rag: rag, WorkerID: "w1", Lease: time.Minute, MaxAttempts: 5, BaseBackoff: time.Second, Clock: clk.Now})
	claimed, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !claimed {
		t.Fatal("RunOnce should have claimed the pending job")
	}
	if status, _ := docStatus(t, ctx, pool, docID); status != "ready" {
		t.Fatalf("status=%q want ready", status)
	}
	if state, _ := jobState(t, ctx, pool, docID); state != "done" {
		t.Fatalf("job state=%q want done", state)
	}
	var chunks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE namespace='ns1'`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks == 0 {
		t.Fatal("no chunks imported")
	}
}

func TestWorkerRunOnceEmptyQueue(t *testing.T) {
	ctx := context.Background()
	pool, rag, _, clk := freshWorkerDB(t, ctx)
	w := NewWorker(WorkerConfig{Pool: pool, Rag: rag, WorkerID: "w1", Lease: time.Minute, MaxAttempts: 5, BaseBackoff: time.Second, Clock: clk.Now})
	claimed, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if claimed {
		t.Fatal("empty queue should claim nothing")
	}
}

// TestWorkerRetryThenDead drives a job whose parse always fails (unsupported
// source type injected directly into the row) through attempts→dead, advancing
// the injected clock past each backoff so the job is due again.
func TestWorkerRetryThenDead(t *testing.T) {
	ctx := context.Background()
	pool, rag, svc, clk := freshWorkerDB(t, ctx)
	// Enqueue a normal job then corrupt its source_type to force parse failure.
	docID, _ := svc.Enqueue(ctx, IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypePaste, Raw: []byte("x")})
	if _, err := pool.Exec(ctx, `UPDATE document SET source_type='bogus' WHERE id=$1`, docID); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(WorkerConfig{Pool: pool, Rag: rag, WorkerID: "w1", Lease: time.Minute, MaxAttempts: 3, BaseBackoff: time.Second, Clock: clk.Now})
	for i := 0; i < 3; i++ {
		claimed, err := w.RunOnce(ctx)
		if err != nil {
			t.Fatalf("RunOnce attempt %d: %v", i, err)
		}
		if !claimed {
			t.Fatalf("attempt %d should have claimed (job due)", i)
		}
		// advance clock past the scheduled backoff so the retry is due.
		clk.Advance(time.Hour)
	}
	state, attempts := jobState(t, ctx, pool, docID)
	if state != "dead" {
		t.Fatalf("job state=%q want dead after maxAttempts", state)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want 3", attempts)
	}
	if status, _ := docStatus(t, ctx, pool, docID); status != "failed" {
		t.Fatalf("doc status=%q want failed", status)
	}
}

// TestWorkerReclaimsStuckLease proves a 'running' job whose lease expired is
// reclaimable by another worker.
func TestWorkerReclaimsStuckLease(t *testing.T) {
	ctx := context.Background()
	pool, rag, svc, clk := freshWorkerDB(t, ctx)
	docID, _ := svc.Enqueue(ctx, IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypePaste, Raw: []byte("the quick brown fox jumps")})
	// Simulate worker A crashing mid-job: mark running with an EXPIRED lease.
	expired := clk.Now().Add(-time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ingest_job SET state='running', locked_by='dead-worker', locked_until=$2, attempts=1 WHERE document_id=$1`, docID, expired); err != nil {
		t.Fatal(err)
	}
	wB := NewWorker(WorkerConfig{Pool: pool, Rag: rag, WorkerID: "wB", Lease: time.Minute, MaxAttempts: 5, BaseBackoff: time.Second, Clock: clk.Now})
	claimed, err := wB.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !claimed {
		t.Fatal("worker B should reclaim the stuck (expired-lease) job")
	}
	if status, _ := docStatus(t, ctx, pool, docID); status != "ready" {
		t.Fatalf("status=%q want ready after reclaim", status)
	}
}

// TestWorkerConcurrentClaimSingleWinner enqueues ONE pending job, then launches
// N goroutines that each call claim() concurrently against the same queue, and
// asserts EXACTLY ONE observes the job. This locks the FOR UPDATE SKIP LOCKED
// no-double-claim guarantee: the losers must get (false, nil), not the job.
func TestWorkerConcurrentClaimSingleWinner(t *testing.T) {
	ctx := context.Background()
	pool, rag, svc, clk := freshWorkerDB(t, ctx)
	docID, err := svc.Enqueue(ctx, IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypePaste, Raw: []byte("the quick brown fox jumps over the lazy dog")})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	const n = 8
	var winners int64
	var claimErr atomic.Value // error
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(n)
	for i := 0; i < n; i++ {
		w := NewWorker(WorkerConfig{Pool: pool, Rag: rag, WorkerID: "w" + string(rune('A'+i)), Lease: time.Minute, MaxAttempts: 5, BaseBackoff: time.Second, Clock: clk.Now})
		go func() {
			defer done.Done()
			start.Wait() // all goroutines released together
			_, ok, err := w.claim(ctx)
			if err != nil {
				claimErr.Store(err)
				return
			}
			if ok {
				atomic.AddInt64(&winners, 1)
			}
		}()
	}
	start.Done() // release the herd
	done.Wait()

	if v := claimErr.Load(); v != nil {
		t.Fatalf("concurrent claim error: %v", v.(error))
	}
	if winners != 1 {
		t.Fatalf("winners=%d want exactly 1 (FOR UPDATE SKIP LOCKED must prevent double-claim)", winners)
	}
	// The single winner left the job 'running' with attempts=1.
	if state, attempts := jobState(t, ctx, pool, docID); state != "running" || attempts != 1 {
		t.Fatalf("after claim: state=%q attempts=%d want running/1", state, attempts)
	}
}

// TestEnqueueDedupConcurrentIdenticalSingleJob proves F4(a): two identical
// enqueues collide on UNIQUE(idempotency_key) so only ONE document+job is
// created and the second returns the first's id (genuine transactional dedup).
func TestEnqueueDedupConcurrentIdenticalSingleJob(t *testing.T) {
	ctx := context.Background()
	pool, _, svc, _ := freshWorkerDB(t, ctx)
	in := IngestInput{KBID: "kb1", Namespace: "ns1", Title: "T", SourceType: SourceTypePaste, Raw: []byte("identical body for dedup")}

	id1, err := svc.Enqueue(ctx, in)
	if err != nil {
		t.Fatalf("Enqueue #1: %v", err)
	}
	// Second identical enqueue: the checksum short-circuit can't match (doc is
	// still 'pending', short-circuit only matches 'ready'), so it reaches the job
	// INSERT and must hit the unique-violation path → returns the existing id.
	id2, err := svc.Enqueue(ctx, in)
	if err != nil {
		t.Fatalf("Enqueue #2 (dedup path): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("dedup: id2=%q want same as id1=%q", id2, id1)
	}
	var docs, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM document WHERE kb_id='kb1'`).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ingest_job`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if docs != 1 || jobs != 1 {
		t.Fatalf("after dedup: docs=%d jobs=%d want 1/1", docs, jobs)
	}
}

// prewarmRecorder wraps a RagPort and records PrewarmCommunityReports calls.
type prewarmRecorder struct {
	ragsvc.RagPort
	mu        sync.Mutex
	prewarmed []string
}

func (r *prewarmRecorder) PrewarmCommunityReports(ctx context.Context, ns string) (int, error) {
	r.mu.Lock()
	r.prewarmed = append(r.prewarmed, ns)
	r.mu.Unlock()
	return 0, nil
}

func TestWorkerPrewarmsAfterImport(t *testing.T) {
	ctx := context.Background()
	pool, rag, svc, clk := freshWorkerDB(t, ctx)
	// Insert a second kb with namespace kb_testkb so we can assert the
	// correct namespace is passed to PrewarmCommunityReports.
	if _, err := pool.Exec(ctx, `INSERT INTO knowledge_base (id, org_id, name, namespace) VALUES ('kb2','o1','KB2','kb_testkb')`); err != nil {
		t.Fatal(err)
	}
	docID, err := svc.Enqueue(ctx, IngestInput{
		KBID:       "kb2",
		Namespace:  "kb_testkb",
		Title:      "T",
		SourceType: SourceTypePaste,
		Raw:        []byte("the quick brown fox jumps over the lazy dog for prewarm test"),
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	rec := &prewarmRecorder{RagPort: rag}
	w := NewWorker(WorkerConfig{Pool: pool, Rag: rec, WorkerID: "t", Lease: time.Minute, MaxAttempts: 3, BaseBackoff: time.Second, Clock: clk.Now})
	ok, runErr := w.RunOnce(ctx)
	if runErr != nil || !ok {
		t.Fatalf("RunOnce ok=%v err=%v", ok, runErr)
	}
	// The document must be ready (prewarm failure must not flip it to failed).
	if status, _ := docStatus(t, ctx, pool, docID); status != "ready" {
		t.Fatalf("doc status=%q want ready", status)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.prewarmed) != 1 || rec.prewarmed[0] != "kb_testkb" {
		t.Fatalf("expected one prewarm for kb_testkb, got %v", rec.prewarmed)
	}
}

// clock is an injectable monotonic-ish clock for deterministic worker tests.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// businessMigrationsForTest mirrors storage.businessMigrations (the worker test
// builds its own DB without importing storage to avoid an import cycle risk).
var businessMigrationsForTest = []string{
	`CREATE TABLE IF NOT EXISTS knowledge_base (id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL, namespace TEXT NOT NULL UNIQUE, embedding_model TEXT NOT NULL DEFAULT '', embedding_dim INT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`CREATE TABLE IF NOT EXISTS document (id TEXT PRIMARY KEY, kb_id TEXT NOT NULL REFERENCES knowledge_base(id) ON DELETE CASCADE, title TEXT NOT NULL, source_type TEXT NOT NULL, source_ref TEXT NOT NULL DEFAULT '', source_id TEXT NOT NULL, checksum TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', phase TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', chunk_count INT NOT NULL DEFAULT 0, content_bytes BIGINT NOT NULL DEFAULT 0, content BYTEA, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`CREATE TABLE IF NOT EXISTS ingest_job (id TEXT PRIMARY KEY, document_id TEXT REFERENCES document(id) ON DELETE CASCADE, state TEXT NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0, next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(), locked_by TEXT NOT NULL DEFAULT '', locked_until TIMESTAMPTZ, idempotency_key TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT '', phase TEXT NOT NULL DEFAULT '', updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ingest_job_idem_idx ON ingest_job (idempotency_key)`,
}
