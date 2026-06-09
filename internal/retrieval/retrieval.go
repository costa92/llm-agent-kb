// Package retrieval turns the HTTP ask request into a RagPort.Ask call and maps
// rag.Answer to the external citation/diagnostics JSON shape (spec §7).
package retrieval

import (
	"context"
	"fmt"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

// Config tunes the retrieval service.
type Config struct {
	MaxAskTokens int // → rag AskOptions.MaxTotalTokens
	SnippetChars int // citation snippet length; <=0 defaults to 240
}

// AskInput is the kb-side ask input.
type AskInput struct {
	Namespace string
	Question  string
	Mode      string // "vector" | "hybrid" (M1 only)
	TopK      int
}

// Citation is the external citation shape (spec §16.2).
type Citation struct {
	ChunkID     string   `json:"chunkId"`
	DocID       string   `json:"docId"`
	Title       string   `json:"title"`
	SectionPath []string `json:"sectionPath,omitempty"`
	Score       float64  `json:"score"`
	Snippet     string   `json:"snippet"`
}

// AskOutput is the external ask response.
type AskOutput struct {
	Answer      string         `json:"answer"`
	Citations   []Citation     `json:"citations"`
	Diagnostics map[string]any `json:"diagnostics"`
}

// Service maps ask requests/responses.
type Service struct {
	rag ragsvc.RagPort
	cfg Config
}

// New builds a retrieval Service.
func New(rag ragsvc.RagPort, cfg Config) *Service {
	if cfg.SnippetChars <= 0 {
		cfg.SnippetChars = 240
	}
	return &Service{rag: rag, cfg: cfg}
}

// Ask runs the vector/hybrid ask and maps citations. Modes other than
// vector/hybrid (global/drift) are M3 and rejected here.
func (s *Service) Ask(ctx context.Context, in AskInput) (AskOutput, error) {
	var hybrid bool
	switch in.Mode {
	case "hybrid":
		hybrid = true
	case "vector":
		hybrid = false
	default:
		return AskOutput{}, fmt.Errorf("retrieval: unsupported mode %q (M1 supports vector|hybrid)", in.Mode)
	}
	topK := in.TopK
	if topK <= 0 {
		topK = 5
	}
	ans, err := s.rag.Ask(ctx, in.Question, ragsvc.AskRequest{
		Namespace:      in.Namespace,
		TopK:           topK,
		Hybrid:         hybrid,
		MaxTotalTokens: s.cfg.MaxAskTokens,
	})
	if err != nil {
		return AskOutput{}, err
	}
	// Snippet source: the matching hit's content, by chunk id.
	snippetByChunk := map[string]string{}
	for _, h := range ans.Hits {
		snippetByChunk[h.Chunk.ID] = h.Chunk.Content
	}
	cites := make([]Citation, 0, len(ans.Citations))
	for _, c := range ans.Citations {
		cites = append(cites, Citation{
			ChunkID:     c.ChunkID,
			DocID:       c.DocID,
			Title:       c.Title,
			SectionPath: c.SectionPath,
			Score:       c.Score,
			Snippet:     truncate(snippetByChunk[c.ChunkID], s.cfg.SnippetChars),
		})
	}
	return AskOutput{
		Answer:    ans.Text,
		Citations: cites,
		Diagnostics: map[string]any{
			"mode":     in.Mode,
			"hitCount": ans.Diagnostics.HitCount,
		},
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
