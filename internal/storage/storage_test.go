package storage

import (
	"context"
	"os"
	"testing"
)

const liveEnvVar = "LLM_AGENT_KB_PG_URL"

func openTestStorage(t *testing.T, ctx context.Context) *Storage {
	t.Helper()
	dsn := os.Getenv(liveEnvVar)
	if dsn == "" {
		t.Skipf("set %s (pgvector-enabled Postgres) to run live tests", liveEnvVar)
	}
	st, err := Open(ctx, Config{PGURL: dsn, EmbeddingDim: 8})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	// Clean slate for deterministic tests.
	for _, tbl := range []string{"ingest_job", "document", "knowledge_base", "chunks", "chunks_entities", "chunks_relations", "chunks_communities", "chunks_community_reports"} {
		_, _ = st.Pool().Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return st
}

func TestMigrateCreatesBusinessAndRagTables(t *testing.T) {
	ctx := context.Background()
	st := openTestStorage(t, ctx)
	if err := st.Migrate(ctx); err != nil { // idempotent second run
		t.Fatalf("second Migrate: %v", err)
	}
	for _, tbl := range []string{"knowledge_base", "document", "chunks"} {
		var n int
		if err := st.Pool().QueryRow(ctx, "SELECT count(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("table %s not queryable after migrate: %v", tbl, err)
		}
	}
}

func TestMigrateCreatesIngestJobAndPhase(t *testing.T) {
	ctx := context.Background()
	st := openTestStorage(t, ctx)
	// ingest_job table is queryable with the §5 columns.
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO ingest_job (id, document_id, state, idempotency_key)
		 SELECT 'j1', NULL, 'pending', 'k1' WHERE false`); err != nil {
		t.Fatalf("ingest_job columns not as expected: %v", err)
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM ingest_job`).Scan(&n); err != nil {
		t.Fatalf("ingest_job not queryable: %v", err)
	}
	// document.phase column exists.
	if err := st.Pool().QueryRow(ctx, `SELECT count(phase) FROM document`).Scan(&n); err != nil {
		t.Fatalf("document.phase missing: %v", err)
	}
	// document.content (BYTEA) column exists — the async worker's loadRaw reads it.
	if err := st.Pool().QueryRow(ctx, `SELECT count(content) FROM document`).Scan(&n); err != nil {
		t.Fatalf("document.content missing: %v", err)
	}
	// idempotency_key is UNIQUE.
	var con int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE tablename='ingest_job' AND indexdef ILIKE '%idempotency_key%'`).Scan(&con); err != nil {
		t.Fatalf("index introspection: %v", err)
	}
	if con == 0 {
		t.Fatal("ingest_job.idempotency_key has no unique index")
	}
}

func TestRagStoreIsUsable(t *testing.T) {
	ctx := context.Background()
	st := openTestStorage(t, ctx)
	if st.RagStore() == nil {
		t.Fatal("RagStore() is nil")
	}
}
