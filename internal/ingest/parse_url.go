package ingest

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"

	readability "github.com/go-shiori/go-readability"

	"github.com/costa92/llm-agent-kb/internal/fetch"
)

// parseURL fetches an HTML page through the SSRF-safe Fetcher and extracts the
// main article text via go-readability. The extracted text is treated as plain
// text downstream (the rag splitter handles structure during Import). Returns
// (text, title, error).
func parseURL(ctx context.Context, f *fetch.Fetcher, rawURL string) (string, string, error) {
	body, _, err := f.Get(ctx, rawURL)
	if err != nil {
		return "", "", err // fetch already wraps with SSRF/transport context
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("ingest: parse url: %w", err)
	}
	art, err := readability.FromReader(bytes.NewReader(body), u)
	if err != nil {
		return "", "", fmt.Errorf("ingest: readability extract %s: %w", rawURL, err)
	}
	text := strings.TrimSpace(art.TextContent)
	if text == "" {
		return "", "", fmt.Errorf("ingest: no readable content extracted from %s", rawURL)
	}
	return text, strings.TrimSpace(art.Title), nil
}
