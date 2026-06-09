package ingest

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ledongthuc/pdf"
)

// parsePDF extracts plain text from a text-based PDF (spec §6: text-only;
// scanned/OCR is out of scope). Pure-Go via ledongthuc/pdf.
func parsePDF(raw []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("ingest: open pdf: %w", err)
	}
	rc, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("ingest: pdf plain text: %w", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return "", fmt.Errorf("ingest: read pdf text: %w", err)
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("ingest: pdf produced no extractable text (scanned/image PDF unsupported)")
	}
	return buf.String(), nil
}
