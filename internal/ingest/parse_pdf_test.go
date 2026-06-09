package ingest

import (
	"os"
	"strings"
	"testing"
)

func TestParsePDFExtractsText(t *testing.T) {
	raw, err := os.ReadFile("testdata/hello.pdf")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	text, err := parsePDF(raw)
	if err != nil {
		t.Fatalf("parsePDF: %v", err)
	}
	if !strings.Contains(text, "Hello PDF") {
		t.Fatalf("extracted text=%q want to contain 'Hello PDF'", text)
	}
}

func TestParsePDFRejectsGarbage(t *testing.T) {
	if _, err := parsePDF([]byte("not a pdf at all")); err == nil {
		t.Fatal("garbage bytes should fail to parse as PDF")
	}
}
