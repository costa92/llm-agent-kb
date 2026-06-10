package sessions

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/costa92/llm-agent-kb/internal/storage"
)

func TestSessions_EnsureAppendListTranscript(t *testing.T) {
	dsn := os.Getenv("LLM_AGENT_KB_PG_URL")
	if dsn == "" {
		t.Skipf("set LLM_AGENT_KB_PG_URL (pgvector) to run the sessions repo test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, tbl := range []string{"qa_message", "qa_session", "eval_run", "document", "knowledge_base"} {
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

	repo := New(pool)
	// EnsureSession with empty id creates one.
	sid, err := repo.EnsureSession(ctx, "kb1", "user1", "", "fox?")
	if err != nil || sid == "" {
		t.Fatalf("ensure: sid=%q err=%v", sid, err)
	}
	// Same id is reused.
	sid2, err := repo.EnsureSession(ctx, "kb1", "user1", sid, "ignored")
	if err != nil || sid2 != sid {
		t.Fatalf("ensure reuse: sid2=%q want %q err=%v", sid2, sid, err)
	}
	if err := repo.AppendPair(ctx, sid, "fox?", "the fox", []byte(`[{"chunkId":"c1"}]`), "hybrid"); err != nil {
		t.Fatalf("append: %v", err)
	}

	list, next, err := repo.ListByKB(ctx, "kb1", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != sid {
		t.Fatalf("list = %+v", list)
	}
	if next != "" {
		t.Fatalf("next = %q want empty", next)
	}

	msgs, err := repo.Transcript(ctx, "kb1", sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("transcript = %+v", msgs)
	}
	if msgs[1].Mode != "hybrid" {
		t.Fatalf("assistant mode = %q", msgs[1].Mode)
	}

	// Isolation: kb2 sees nothing.
	if l, _, _ := repo.ListByKB(ctx, "kb2", 10, ""); len(l) != 0 {
		t.Fatalf("kb2 leak: %+v", l)
	}
	if _, err := repo.Transcript(ctx, "kb2", sid); err == nil {
		t.Fatal("kb2 transcript of kb1 session must error (not found)")
	}
}
