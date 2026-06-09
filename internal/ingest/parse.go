package ingest

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/costa92/llm-agent-kb/internal/fetch"
)

// SourceType is the accepted document source set (M1 text + M2 pdf/docx/url).
type SourceType string

const (
	SourceTypeMarkdown SourceType = "markdown"
	SourceTypeTXT      SourceType = "txt"
	SourceTypePaste    SourceType = "paste"
	SourceTypePDF      SourceType = "pdf"
	SourceTypeDOCX     SourceType = "docx"
	SourceTypeURL      SourceType = "url"
)

// parseDeps carries the runtime collaborators a parse needs.
type parseDeps struct {
	fetcher      *fetch.Fetcher // for SourceTypeURL
	parseTimeout time.Duration  // deadline around PDF/DOCX parse (anti parse-bomb)
}

// parseSource converts a source's raw bytes (or, for URL, the URL in sourceRef)
// into (text, title, error). md/txt/paste are read as text; pdf/docx are parsed
// under a parse-timeout; url is fetched (SSRF-safe) + readability-extracted.
func parseSource(ctx context.Context, deps parseDeps, st SourceType, raw []byte, sourceRef string) (string, string, error) {
	switch st {
	case SourceTypeMarkdown, SourceTypeTXT, SourceTypePaste:
		return string(raw), "", nil
	case SourceTypePDF:
		return withParseTimeout(ctx, deps.parseTimeout, func() (string, error) { return parsePDF(raw) })
	case SourceTypeDOCX:
		return withParseTimeout(ctx, deps.parseTimeout, func() (string, error) { return parseDOCX(raw) })
	case SourceTypeURL:
		if deps.fetcher == nil {
			return "", "", fmt.Errorf("ingest: url source requires a fetcher")
		}
		return parseURL(ctx, deps.fetcher, sourceRef)
	default:
		return "", "", fmt.Errorf("ingest: unsupported source_type %q", st)
	}
}

// withParseTimeout runs a synchronous CPU-bound parse with a deadline. If the
// context is already done OR the timeout elapses, it returns the context error
// (the parse goroutine is abandoned — acceptable for a bounded text extract).
func withParseTimeout(ctx context.Context, timeout time.Duration, fn func() (string, error)) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if timeout <= 0 {
		text, err := fn()
		return text, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type res struct {
		text string
		err  error
	}
	ch := make(chan res, 1)
	go func() { t, e := fn(); ch <- res{t, e} }()
	select {
	case <-cctx.Done():
		return "", "", fmt.Errorf("ingest: parse timed out: %w", cctx.Err())
	case r := <-ch:
		return r.text, "", r.err
	}
}

// allowedExtensions maps the upload extension allowlist (§16.3): pdf/md/txt/docx.
var allowedExtensions = map[string]bool{
	".pdf": true, ".md": true, ".markdown": true, ".txt": true, ".docx": true,
}

// ValidateUpload enforces the §16.3 upload allowlist: the filename extension
// must be in the allowlist and size must be within maxBytes. (Content-type is
// not checked — clients lie; the extension governs, and the actual parse in
// Task 4/5 is the real validator.) Exported because the httpapi uploadHandler
// (Task 9, another package) calls it; this is the FINAL signature used everywhere.
func ValidateUpload(filename string, size, maxBytes int64) error {
	if size > maxBytes {
		return fmt.Errorf("ingest: upload %d bytes exceeds max %d", size, maxBytes)
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedExtensions[ext] {
		return fmt.Errorf("ingest: extension %q not allowed (pdf/md/txt/docx only)", ext)
	}
	return nil
}
