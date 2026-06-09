package ingest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/costa92/llm-agent-kb/internal/fetch"
)

// minimalPDF holds the bytes of the "Hello PDF" test fixture.
var minimalPDF []byte

func TestMain(m *testing.M) {
	var err error
	minimalPDF, err = os.ReadFile("testdata/hello.pdf")
	if err != nil {
		panic("parse_test: read testdata/hello.pdf: " + err.Error())
	}
	os.Exit(m.Run())
}

func TestParseSourceTextTypes(t *testing.T) {
	deps := parseDeps{fetcher: nil, parseTimeout: time.Second}
	for _, st := range []SourceType{SourceTypeMarkdown, SourceTypeTXT, SourceTypePaste} {
		text, _, err := parseSource(context.Background(), deps, st, []byte("# hi\nbody"), "")
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if !strings.Contains(text, "body") {
			t.Fatalf("%s text=%q", st, text)
		}
	}
}

func TestParseSourcePDF(t *testing.T) {
	deps := parseDeps{parseTimeout: 5 * time.Second}
	text, _, err := parseSource(context.Background(), deps, SourceTypePDF, minimalPDF, "")
	if err != nil {
		t.Fatalf("pdf: %v", err)
	}
	if !strings.Contains(text, "Hello PDF") {
		t.Fatalf("pdf text=%q", text)
	}
}

func TestParseSourceUnsupported(t *testing.T) {
	if _, _, err := parseSource(context.Background(), parseDeps{}, SourceType("bogus"), nil, ""); err == nil {
		t.Fatal("unknown source type must error")
	}
}

func TestValidateUpload(t *testing.T) {
	// allowed
	if err := ValidateUpload("report.pdf", 100, 1<<20); err != nil {
		t.Fatalf("pdf should be allowed: %v", err)
	}
	if err := ValidateUpload("notes.md", 100, 1<<20); err != nil {
		t.Fatalf("md should be allowed: %v", err)
	}
	// disallowed extension
	if err := ValidateUpload("evil.exe", 100, 1<<20); err == nil {
		t.Fatal("exe must be rejected")
	}
	// over size
	if err := ValidateUpload("big.pdf", 2<<20, 1<<20); err == nil {
		t.Fatal("oversize must be rejected")
	}
}

func TestParseTimeoutFires(t *testing.T) {
	// A canceled context makes the PDF parse return a deadline error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps := parseDeps{parseTimeout: time.Hour}
	if _, _, err := parseSource(ctx, deps, SourceTypePDF, minimalPDF, ""); err == nil {
		t.Fatal("canceled context should abort parse")
	}
}

var _ = fetch.New // keep import even if fetcher unused in some subtests
