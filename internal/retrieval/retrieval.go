// Package retrieval turns the HTTP ask request into a RagPort.Ask call and maps
// rag.Answer to the external citation/diagnostics JSON shape (spec §7).
package retrieval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

// Config tunes the retrieval service.
type Config struct {
	MaxAskTokens int // → rag AskOptions.MaxTotalTokens
	SnippetChars int // citation snippet length; <=0 defaults to 240

	GlobalMaxCommunities int // GlobalOptions.MaxCommunities when the request omits it
	DriftRounds          int // DriftOptions.Rounds default
	DriftTopK            int // DriftOptions.TopK default
}

// AskInput is the kb-side ask input.
type AskInput struct {
	Namespace string
	KBID      string
	UserID    string
	SessionID string // optional; empty = create-on-first-ask
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
	SessionID   string         `json:"sessionId,omitempty"`
}

// Service maps ask requests/responses.
type Service struct {
	rag      ragsvc.RagPort
	cfg      Config
	recorder Recorder
}

// New builds a retrieval Service.
func New(rag ragsvc.RagPort, cfg Config) *Service {
	if cfg.SnippetChars <= 0 {
		cfg.SnippetChars = 240
	}
	return &Service{rag: rag, cfg: cfg}
}

// Recorder persists Q&A history (satisfied by *sessions.Repo). nil disables
// persistence (focused unit tests). Kept a narrow interface so retrieval has no
// DB dependency.
type Recorder interface {
	EnsureSession(ctx context.Context, kbID, userID, sessionID, firstQuestion string) (string, error)
	AppendPair(ctx context.Context, sessionID, question, answer string, citationsJSON []byte, mode string) error
}

// SetRecorder wires history persistence after construction (cmd/kbd sets it).
func (s *Service) SetRecorder(r Recorder) { s.recorder = r }

// persist records the q/a pair into a session (create-on-first-ask). Best effort
// for the session id but errors propagate so callers see a 500 on a broken DB;
// a nil recorder is a no-op (returns the inbound sessionID unchanged).
func (s *Service) persist(ctx context.Context, kbID, userID, sessionID, question, mode string, out AskOutput) (string, error) {
	if s.recorder == nil {
		return sessionID, nil
	}
	sid, err := s.recorder.EnsureSession(ctx, kbID, userID, sessionID, question)
	if err != nil {
		return "", err
	}
	citesJSON, _ := json.Marshal(out.Citations)
	if err := s.recorder.AppendPair(ctx, sid, question, out.Answer, citesJSON, mode); err != nil {
		return "", err
	}
	return sid, nil
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
	out := AskOutput{
		Answer:    ans.Text,
		Citations: cites,
		Diagnostics: map[string]any{
			"mode":     in.Mode,
			"hitCount": ans.Diagnostics.HitCount,
		},
	}
	sid, err := s.persist(ctx, in.KBID, in.UserID, in.SessionID, in.Question, in.Mode, out)
	if err != nil {
		return AskOutput{}, err
	}
	out.SessionID = sid
	return out, nil
}

// GlobalInput is the kb-side AskGlobal input (spec §7). Isolation is
// Namespace-ONLY (§8) — no SecurityFilters on the global path.
type GlobalInput struct {
	Namespace      string
	KBID           string
	UserID         string
	SessionID      string
	Question       string
	MaxCommunities int
}

// DriftInput is the kb-side AskDrift input (spec §7). Namespace-only (§8).
type DriftInput struct {
	Namespace      string
	KBID           string
	UserID         string
	SessionID      string
	Question       string
	MaxCommunities int
	Rounds         int
	TopK           int
}

// AskGlobal runs GraphRAG global map-reduce. The Answer carries no Citations
// (global answers are community-level), so AskOutput.Citations is empty.
func (s *Service) AskGlobal(ctx context.Context, in GlobalInput) (AskOutput, error) {
	maxC := in.MaxCommunities
	if maxC <= 0 {
		maxC = s.cfg.GlobalMaxCommunities
	}
	ans, err := s.rag.AskGlobal(ctx, in.Question, ragsvc.GlobalRequest{
		Namespace:      in.Namespace,
		MaxCommunities: maxC,
		MaxTotalTokens: s.cfg.MaxAskTokens,
	})
	if err != nil {
		return AskOutput{}, err
	}
	out := AskOutput{
		Answer:    ans.Text,
		Citations: []Citation{},
		Diagnostics: map[string]any{
			"mode":         "global",
			"communityIds": ans.Diagnostics.Global.CommunityIDs,
			"mapCalls":     ans.Diagnostics.Global.MapCalls,
			"reduceCalls":  ans.Diagnostics.Global.ReduceCalls,
		},
	}
	sid, err := s.persist(ctx, in.KBID, in.UserID, in.SessionID, in.Question, "global", out)
	if err != nil {
		return AskOutput{}, err
	}
	out.SessionID = sid
	return out, nil
}

// AskDrift runs GraphRAG drift (global primer + local follow-up). No Citations.
func (s *Service) AskDrift(ctx context.Context, in DriftInput) (AskOutput, error) {
	rounds := in.Rounds
	if rounds <= 0 {
		rounds = s.cfg.DriftRounds
	}
	topK := in.TopK
	if topK <= 0 {
		topK = s.cfg.DriftTopK
	}
	maxC := in.MaxCommunities
	if maxC <= 0 {
		maxC = s.cfg.GlobalMaxCommunities
	}
	ans, err := s.rag.AskDrift(ctx, in.Question, ragsvc.DriftRequest{
		Namespace:      in.Namespace,
		MaxCommunities: maxC,
		Rounds:         rounds,
		TopK:           topK,
		MaxTotalTokens: s.cfg.MaxAskTokens,
	})
	if err != nil {
		return AskOutput{}, err
	}
	out := AskOutput{
		Answer:    ans.Text,
		Citations: []Citation{},
		Diagnostics: map[string]any{
			"mode":               "drift",
			"primerCommunityIds": ans.Diagnostics.Drift.PrimerCommunityIDs,
			"rounds":             ans.Diagnostics.Drift.Rounds,
		},
	}
	sid, err := s.persist(ctx, in.KBID, in.UserID, in.SessionID, in.Question, "drift", out)
	if err != nil {
		return AskOutput{}, err
	}
	out.SessionID = sid
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
