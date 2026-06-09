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
	for _, tbl := range []string{"document", "knowledge_base", "chunks", "chunks_entities", "chunks_relations", "chunks_communities", "chunks_community_reports"} {
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

func TestRagStoreIsUsable(t *testing.T) {
	ctx := context.Background()
	st := openTestStorage(t, ctx)
	if st.RagStore() == nil {
		t.Fatal("RagStore() is nil")
	}
}
