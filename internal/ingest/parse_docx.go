package ingest

import (
	"bytes"
	"fmt"
	"strings"

	docx "github.com/fumiama/go-docx"
)

// parseDOCX extracts plain text from an OOXML .docx. Pure-Go via fumiama/go-docx.
// It walks Body.Items (paragraphs) → Run children → Text children, joining
// paragraphs with newlines. Tables/images are skipped (text-only, spec §6).
func parseDOCX(raw []byte) (string, error) {
	doc, err := docx.Parse(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("ingest: parse docx: %w", err)
	}
	var paragraphs []string
	for _, item := range doc.Document.Body.Items {
		p, ok := item.(*docx.Paragraph)
		if !ok {
			continue
		}
		var sb strings.Builder
		for _, child := range p.Children {
			run, ok := child.(*docx.Run)
			if !ok {
				continue
			}
			for _, rc := range run.Children {
				if txt, ok := rc.(*docx.Text); ok {
					sb.WriteString(txt.Text)
				}
			}
		}
		if line := strings.TrimSpace(sb.String()); line != "" {
			paragraphs = append(paragraphs, line)
		}
	}
	if len(paragraphs) == 0 {
		return "", fmt.Errorf("ingest: docx produced no extractable text")
	}
	return strings.Join(paragraphs, "\n\n"), nil
}
