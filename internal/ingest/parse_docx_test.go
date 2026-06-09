package ingest

import (
	"bytes"
	"strings"
	"testing"

	docx "github.com/fumiama/go-docx"
)

// buildDocx returns a valid .docx byte stream with two paragraphs.
func buildDocx(t *testing.T) []byte {
	t.Helper()
	d := docx.New()
	d.AddParagraph().AddText("First paragraph about foxes.")
	d.AddParagraph().AddText("Second paragraph about dogs.")
	var buf bytes.Buffer
	if _, err := d.WriteTo(&buf); err != nil {
		t.Fatalf("write docx: %v", err)
	}
	return buf.Bytes()
}

func TestParseDOCXExtractsParagraphs(t *testing.T) {
	raw := buildDocx(t)
	text, err := parseDOCX(raw)
	if err != nil {
		t.Fatalf("parseDOCX: %v", err)
	}
	if !strings.Contains(text, "First paragraph about foxes.") ||
		!strings.Contains(text, "Second paragraph about dogs.") {
		t.Fatalf("extracted text missing paragraphs: %q", text)
	}
}

func TestParseDOCXRejectsGarbage(t *testing.T) {
	if _, err := parseDOCX([]byte("PK\x03\x04 but not really a docx")); err == nil {
		t.Fatal("garbage should fail to parse as docx")
	}
}
