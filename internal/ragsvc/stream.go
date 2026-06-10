package ragsvc

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/costa92/llm-agent-contract/llm"
	ragprompt "github.com/costa92/llm-agent-rag/prompt"
	ragcore "github.com/costa92/llm-agent-rag/rag"
)

// StreamRequest is the kb-side streaming-ask request. Mirrors AskRequest, but
// note rag's Retrieve path ignores EnableRerank (rerank runs only in Ask/pack),
// so Hybrid here only labels the diagnostics mode — it does NOT rerank the
// stream hits (rerank parity with non-stream /ask is out of M5a scope).
type StreamRequest struct {
	Namespace string
	TopK      int
	Hybrid    bool
}

// StreamEventKind enumerates the kb-local streaming event variants. Kept
// kb-local (NOT llm.StreamEventKind) so retrieval/httpapi never import the
// contract stream types — ragsvc stays the boundary (spec §4).
type StreamEventKind uint8

const (
	StreamEventToken StreamEventKind = iota // Text holds one answer delta
	StreamEventDone                         // terminal; Citations + HitCount populated
)

// StreamCitation is the kb-local projection of a retrieved hit, flattened so
// importers never see rag/store types. Mirrors the fields retrieval.Citation
// maps to the external JSON shape.
type StreamCitation struct {
	ChunkID     string
	DocID       string
	Title       string
	SectionPath []string
	Score       float64
	Snippet     string
}

// StreamEvent is the kb-local streaming union. Field population is gated by
// Kind: StreamEventToken → Text; StreamEventDone → Citations + HitCount.
type StreamEvent struct {
	Kind      StreamEventKind
	Text      string
	Citations []StreamCitation
	HitCount  int
}

// StreamAnswer runs a single-pass grounded streaming answer (Option A): it
// retrieves context via the held rag System, renders the SAME prompt the rag
// default QA template uses, then streams the chat model's tokens. Token deltas
// are delivered to emit as they arrive; a terminal StreamEventDone carries the
// citations derived from the retrieved hits. This deliberately bypasses rag's
// reflection/grader orchestration (M5a tradeoff) — the non-stream Ask path
// keeps the full pipeline.
func (s *Service) StreamAnswer(ctx context.Context, question string, req StreamRequest, emit func(StreamEvent) error) error {
	ctx, span := s.tracer.Start(ctx, "ragsvc.StreamAnswer")
	defer span.End()
	hits, err := s.wrapper.Retrieve(ctx, question, ragcore.SearchOptions{
		Namespace: req.Namespace,
		TopK:      req.TopK,
		// EnableRerank is set for symmetry but is a NO-OP here: rag's Retrieve
		// path ignores it (rerank runs only in Ask/pack), so Hybrid does not
		// change the stream hits — it only labels the diagnostics mode upstream.
		EnableRerank: req.Hybrid,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("ragsvc: stream retrieve: %w", err)
	}
	// Render the grounded prompt with rag's own default QA template over the
	// retrieved hits — same template rag.System.askRound renders by default,
	// so the prompt shape stays faithful to the non-stream path.
	genReq, err := ragprompt.DefaultQATemplate{}.Render(ctx, ragprompt.RenderContext{
		Question:  question,
		Namespace: req.Namespace,
		Hits:      hits,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("ragsvc: stream render: %w", err)
	}
	llmReq := llm.Request{SystemPrompt: genReq.SystemPrompt}
	for _, m := range genReq.Messages {
		llmReq.Messages = append(llmReq.Messages, llm.Message{Role: m.Role, Content: m.Content})
	}
	sr, err := s.model.Stream(ctx, llmReq)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("ragsvc: stream model: %w", err)
	}
	defer sr.Close()
	for {
		ev, err := sr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			span.RecordError(err)
			return fmt.Errorf("ragsvc: stream next: %w", err)
		}
		if ev.Kind == llm.EventTextDelta && ev.Text != "" {
			if cbErr := emit(StreamEvent{Kind: StreamEventToken, Text: ev.Text}); cbErr != nil {
				return cbErr
			}
		}
		// EventDone / tool / thinking deltas: ignored for the QA stream.
	}
	cites := make([]StreamCitation, 0, len(hits))
	for _, h := range hits {
		cites = append(cites, StreamCitation{
			ChunkID:     h.Chunk.ID,
			DocID:       h.Chunk.DocID,
			Title:       h.Chunk.Title,
			SectionPath: append([]string(nil), h.Chunk.SectionPath...),
			Score:       h.Score,
			Snippet:     h.Chunk.Content,
		})
	}
	return emit(StreamEvent{Kind: StreamEventDone, Citations: cites, HitCount: len(hits)})
}
