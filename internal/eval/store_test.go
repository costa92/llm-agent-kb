package eval

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-kb/internal/storage"
)

func TestEvalRunStore_InsertListLatest(t *testing.T) {
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the eval_run repo test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// This test owns its tables — drop then migrate (DROP TABLE, never SCHEMA,
	// so the pgvector extension survives). storage.Open ensures the extension.
	for _, tbl := range []string{"eval_run", "qa_message", "qa_session", "document", "knowledge_base"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	st, err := storage.Open(ctx, storage.Config{PGURL: dsn, EmbeddingDim: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	repo := NewStore(pool)
	id1, err := repo.Insert(ctx, InsertInput{
		KBID: "kb1", Kind: KindRetrieval, DatasetName: "ds",
		MetricsJSON: []byte(`{"precisionAtK":0.5}`),
	})
	if err != nil || id1 == "" {
		t.Fatalf("insert1: id=%q err=%v", id1, err)
	}
	if _, err := repo.Insert(ctx, InsertInput{
		KBID: "kb1", Kind: KindDrift, DatasetName: "ds",
		MetricsJSON: []byte(`{"benchmark":{"x":1}}`), DriftJSON: []byte(`{"dataset":"ds"}`),
	}); err != nil {
		t.Fatalf("insert2: %v", err)
	}

	rows, next, err := repo.ListByKB(ctx, "kb1", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListByKB = %d rows, want 2", len(rows))
	}
	if next != "" {
		t.Fatalf("next cursor = %q, want empty (page not full)", next)
	}

	// Latest drift-kind benchmark for kb1+ds is the second insert. metrics_json
	// is JSONB, which normalizes whitespace, so compare the decoded value (the
	// real contract: LatestBenchmark's bytes must unmarshal back to the stored
	// struct for the drift baseline), not the raw byte string.
	raw, ok, err := repo.LatestBenchmark(ctx, "kb1", "ds")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("LatestBenchmark ok=%v", ok)
	}
	var got, want any
	_ = json.Unmarshal(raw, &got)
	_ = json.Unmarshal([]byte(`{"benchmark":{"x":1}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LatestBenchmark decoded=%v want=%v (raw=%s)", got, want, raw)
	}

	// Isolation: a different kb sees nothing.
	if _, ok, _ := repo.LatestBenchmark(ctx, "kb2", "ds"); ok {
		t.Fatal("kb2 must not see kb1's benchmark")
	}
}
