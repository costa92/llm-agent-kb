package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	ragingest "github.com/costa92/llm-agent-rag/ingest"
)

func TestParseTXTAndPasteArePlainText(t *testing.T) {
	deps := parseDeps{parseTimeout: time.Second}
	got, _, err := parseSource(context.Background(), deps, SourceTypeTXT, []byte("line one\nline two"), "")
	if err != nil || got != "line one\nline two" {
		t.Fatalf("parse txt=%q,%v", got, err)
	}
	got, _, err = parseSource(context.Background(), deps, SourceTypePaste, []byte("pasted"), "")
	if err != nil || got != "pasted" {
		t.Fatalf("parse paste=%q,%v", got, err)
	}
}

func TestParseMarkdownKeptVerbatim(t *testing.T) {
	md := "# Title\n\nbody text"
	deps := parseDeps{parseTimeout: time.Second}
	got, _, err := parseSource(context.Background(), deps, SourceTypeMarkdown, []byte(md), "")
	if err != nil || got != md {
		t.Fatalf("parse md=%q,%v", got, err)
	}
}

func TestParseRejectsUnsupportedType(t *testing.T) {
	if _, _, err := parseSource(context.Background(), parseDeps{}, SourceType("bogus"), []byte("x"), ""); err == nil {
		t.Fatal("unknown source type must error")
	}
}

func TestMakeDocumentSetsSourceIDToDocIDAndMetadata(t *testing.T) {
	doc := makeDocument("doc-1", "kb-9", SourceTypeMarkdown, "My Title", "# H\n\ntext")
	if doc.ID != "doc-1" || doc.SourceID != "doc-1" {
		t.Fatalf("ID/SourceID=%q/%q want doc-1/doc-1", doc.ID, doc.SourceID)
	}
	if doc.Title != "My Title" {
		t.Fatalf("Title=%q", doc.Title)
	}
	if doc.Checksum == "" {
		t.Fatal("Checksum must be set for the unchanged-skip short-circuit")
	}
	// doc_id must NOT be in metadata (citation DocID comes from Chunk.DocID).
	if _, exists := doc.Metadata["doc_id"]; exists {
		t.Fatal("metadata must NOT carry doc_id (spec §6 / §7)")
	}
	if doc.Metadata["kb_id"] != "kb-9" || doc.Metadata["source_type"] != string(SourceTypeMarkdown) {
		t.Fatalf("metadata=%v want kb_id/source_type", doc.Metadata)
	}
	var _ ragingest.Document = doc
}

func TestChecksumStableAndContentSensitive(t *testing.T) {
	a := checksum("hello")
	b := checksum("hello")
	c := checksum("world")
	if a != b {
		t.Fatal("checksum must be stable for identical content")
	}
	if a == c {
		t.Fatal("checksum must differ for different content")
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("checksum=%q want sha256: prefix", a)
	}
}
