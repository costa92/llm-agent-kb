package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/costa92/llm-agent-kb/internal/fetch"
)

func TestParseURLExtractsArticleText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Foxes</title></head><body>
			<article><h1>The Fox</h1><p>The quick brown fox jumps over the lazy dog. ` +
			strings.Repeat("This is a long article body so readability keeps it. ", 20) +
			`</p></article></body></html>`))
	}))
	defer srv.Close()

	f := fetch.NewLoopbackForTest(time.Second, 1<<20, []string{"text/html"})
	text, title, err := parseURL(context.Background(), f, srv.URL)
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	if !strings.Contains(text, "quick brown fox") {
		t.Fatalf("text missing article body: %q", text)
	}
	if title == "" {
		t.Fatalf("title not extracted")
	}
}

func TestParseURLPropagatesFetchError(t *testing.T) {
	f := fetch.NewLoopbackForTest(time.Second, 1<<20, []string{"text/html"})
	if _, _, err := parseURL(context.Background(), f, "http://10.0.0.1/"); err == nil {
		t.Fatal("private URL must error through parseURL")
	}
}
