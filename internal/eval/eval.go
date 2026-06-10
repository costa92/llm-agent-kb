// Package eval is the kb-level quality use case (§9, §13 M4). It is — alongside
// ragsvc — a permitted importer of rag/eval (spec §4): the rag evaluators are
// struct literals parameterized by rag interfaces, so the glue that builds them
// must see rag types. The OUTWARD boundary is preserved: this package exports
// only kb-local DTOs (EvalResult / MetricsView / GenerationView / DriftView);
// httpapi/retrieval/sessions/cmd-kbd never import rag/eval.
package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	rageval "github.com/costa92/llm-agent-rag/eval"
	raggenerate "github.com/costa92/llm-agent-rag/generate"
	ragcore "github.com/costa92/llm-agent-rag/rag"
)

// Kind enumerates the supported eval kinds (§5 eval_run.kind).
type Kind string

const (
	KindRetrieval Kind = "retrieval"
	KindTriad     Kind = "triad"
	KindGlobal    Kind = "global"
	KindDrift     Kind = "drift"
)

// MetricsView is the kb-local projection of eval.Metrics (retrieval leg).
type MetricsView struct {
	PrecisionAtK float64 `json:"precisionAtK"`
	RecallAtK    float64 `json:"recallAtK"`
	MRR          float64 `json:"mrr"`
	GroundingAtK float64 `json:"groundingAtK"`
	Examples     int     `json:"examples"`
	TopK         int     `json:"topK"`
}

// GenerationView is the kb-local projection of the generation-side legs
// (triad/global/drift share groundedness + answer relevance).
type GenerationView struct {
	MeanGroundedness    float64 `json:"meanGroundedness"`
	MeanAnswerRelevance float64 `json:"meanAnswerRelevance"`
	Examples            int     `json:"examples"`
}

// MetricDeltaView is the kb-local projection of one eval.MetricDelta. NaN
// scalars (the rag "feature off everywhere" sentinel) serialize to JSON null
// via *float64, since math.NaN() does not round-trip through encoding/json.
type MetricDeltaView struct {
	Name      string   `json:"name"`
	Prev      *float64 `json:"prev"`
	Curr      *float64 `json:"curr"`
	Delta     *float64 `json:"delta"`
	Direction string   `json:"direction"`
}

// HistogramDeltaView is the kb-local projection of one eval.HistogramDelta
// (§9 drift dashboard). All fields are JSON-friendly (no NaN): two int-bucket
// histograms + their per-bucket delta + the L1 distance.
type HistogramDeltaView struct {
	Name       string  `json:"name"`
	Prev       []int   `json:"prev"`
	Curr       []int   `json:"curr"`
	Delta      []int   `json:"delta"`
	L1Distance float64 `json:"l1Distance"`
}

// DriftView is the kb-local projection of eval.DriftReport.
type DriftView struct {
	Dataset         string               `json:"dataset"`
	Deltas          []MetricDeltaView    `json:"deltas"`
	Histograms      []HistogramDeltaView `json:"histograms"`
	NewExamples     []string             `json:"newExamples"`
	DroppedExamples []string             `json:"droppedExamples"`
}

// EvalResult is the kb-local result of one eval run. Exactly one of the metric
// views is populated per kind; Drift is set only for KindDrift.
type EvalResult struct {
	Kind        Kind            `json:"kind"`
	DatasetName string          `json:"datasetName"`
	Retrieval   *MetricsView    `json:"retrieval,omitempty"`
	Generation  *GenerationView `json:"generation,omitempty"`
	Drift       *DriftView      `json:"drift,omitempty"`
}

func metricsFromRetrieval(m rageval.Metrics) MetricsView {
	return MetricsView{
		PrecisionAtK: m.PrecisionAtK,
		RecallAtK:    m.RecallAtK,
		MRR:          m.MRR,
		GroundingAtK: m.GroundingAtK,
		Examples:     m.Examples,
		TopK:         m.TopK,
	}
}

func metricsFromGeneration(groundedness, relevance float64, examples int) GenerationView {
	return GenerationView{
		MeanGroundedness:    groundedness,
		MeanAnswerRelevance: relevance,
		Examples:            examples,
	}
}

// nullableFloat maps NaN→nil so encoding/json emits null instead of failing.
func nullableFloat(f float64) *float64 {
	if math.IsNaN(f) {
		return nil
	}
	return &f
}

func driftView(r rageval.DriftReport) DriftView {
	deltas := make([]MetricDeltaView, 0, len(r.Deltas))
	for _, d := range r.Deltas {
		deltas = append(deltas, MetricDeltaView{
			Name:      d.Name,
			Prev:      nullableFloat(d.Prev),
			Curr:      nullableFloat(d.Curr),
			Delta:     nullableFloat(d.Delta),
			Direction: string(d.Direction),
		})
	}
	hists := make([]HistogramDeltaView, 0, len(r.Histograms))
	for _, h := range r.Histograms {
		hists = append(hists, HistogramDeltaView{
			Name:       h.Name,
			Prev:       h.Prev,
			Curr:       h.Curr,
			Delta:      h.Delta,
			L1Distance: h.L1Distance,
		})
	}
	newEx := r.NewExamples
	if newEx == nil {
		newEx = []string{}
	}
	dropped := r.DroppedExamples
	if dropped == nil {
		dropped = []string{}
	}
	return DriftView{
		Dataset:         r.Dataset,
		Deltas:          deltas,
		Histograms:      hists,
		NewExamples:     newEx,
		DroppedExamples: dropped,
	}
}

// ServiceConfig tunes the eval use case.
type ServiceConfig struct {
	MaxAskTokens         int // rag MaxTotalTokens budget for triad/global/drift asks
	GlobalMaxCommunities int // GlobalEvaluator.MaxCommunities / DriftEvaluator.MaxCommunities
	DriftRounds          int // DriftEvaluator.Rounds
	DriftTopK            int // DriftEvaluator dataset TopK fallback
}

// Service is the kb eval use case. It builds rag's evaluators over a RagPort +
// a judge model and projects results to kb-local DTOs.
type Service struct {
	port  Port
	judge raggenerate.Model
	cfg   ServiceConfig
}

// NewService builds the eval use case. judge is the rag generate.Model seam
// (ragsvc.JudgeModel()); port is ragsvc.RagPort.
func NewService(port Port, judge raggenerate.Model, cfg ServiceConfig) *Service {
	return &Service{port: port, judge: judge, cfg: cfg}
}

// RunRequest is one eval invocation. Dataset is already parsed (httpapi parses
// inline JSONL via LoadDataset). Prev is supplied by the caller for drift via
// the eval_run store; the Service itself is store-free for unit-testability.
type RunRequest struct {
	KBID      string
	Namespace string
	Kind      Kind
	Dataset   rageval.Dataset
	// PrevBenchmarkJSON is the previously stored BenchmarkResult metrics_json
	// (from Store.LatestBenchmark) for drift compare; nil = no baseline (first run).
	PrevBenchmarkJSON []byte
}

// Run executes the eval for the request's kind and returns a kb-local result.
// For drift it also returns the current BenchmarkResult JSON so the caller can
// persist it as the next baseline (CurrBenchmarkJSON).
func (s *Service) Run(ctx context.Context, req RunRequest) (EvalResult, error) {
	switch req.Kind {
	case KindRetrieval:
		ev := rageval.RetrievalEvaluator{
			Retriever: retrieverAdapter{port: s.port, namespace: req.Namespace},
			Options:   ragcore.SearchOptions{Namespace: req.Namespace},
		}
		r, err := ev.Run(ctx, req.Dataset)
		if err != nil {
			return EvalResult{}, fmt.Errorf("eval: retrieval run: %w", err)
		}
		mv := metricsFromRetrieval(r.Metrics)
		return EvalResult{Kind: KindRetrieval, DatasetName: req.Dataset.Name, Retrieval: &mv}, nil

	case KindTriad:
		ev := rageval.TriadEvaluator{
			Asker:   askerAdapter{port: s.port, namespace: req.Namespace, maxTokens: s.cfg.MaxAskTokens},
			Judge:   rageval.LLMJudge{Model: s.judge},
			Options: ragcore.AskOptions{Search: ragcore.SearchOptions{Namespace: req.Namespace}},
		}
		r, err := ev.Run(ctx, req.Dataset)
		if err != nil {
			return EvalResult{}, fmt.Errorf("eval: triad run: %w", err)
		}
		gv := metricsFromGeneration(r.Generation.MeanGroundedness, r.Generation.MeanAnswerRelevance, r.Generation.Examples)
		return EvalResult{Kind: KindTriad, DatasetName: req.Dataset.Name, Generation: &gv}, nil

	case KindGlobal:
		ev := rageval.GlobalEvaluator{
			Asker:          globalAskerAdapter{port: s.port, namespace: req.Namespace, maxTokens: s.cfg.MaxAskTokens},
			Judge:          rageval.LLMJudge{Model: s.judge},
			MaxCommunities: s.cfg.GlobalMaxCommunities,
		}
		r, err := ev.Run(ctx, req.Dataset)
		if err != nil {
			return EvalResult{}, fmt.Errorf("eval: global run: %w", err)
		}
		gv := metricsFromGeneration(r.MeanGroundedness, r.MeanAnswerRelevance, r.Examples)
		return EvalResult{Kind: KindGlobal, DatasetName: req.Dataset.Name, Generation: &gv}, nil

	case KindDrift:
		// runDrift returns (result, baselineJSON, err); Run discards the baseline
		// (only the Runner persists it). The drift result still serializes here.
		res, _, err := s.runDrift(ctx, req)
		return res, err

	default:
		return EvalResult{}, fmt.Errorf("eval: unsupported kind %q", req.Kind)
	}
}

// runDrift produces the current BenchmarkResult via AnswerBenchmark over the
// local ask path, compares against the previous stored baseline
// (PrevBenchmarkJSON), projects the DriftReport, AND returns the scrubbed curr
// BenchmarkResult JSON so the caller persists it as the next baseline. When
// there is no baseline, Deltas compare against a zero-value benchmark (prev=NaN
// sentinels) — the report still serializes; Direction reads "undefined" for
// unscored metrics.
func (s *Service) runDrift(ctx context.Context, req RunRequest) (EvalResult, []byte, error) {
	// Build the answer dataset from the retrieval dataset (gold answers absent
	// → textual metrics degenerate; drift here tracks groundedness/relevance).
	answerDS := rageval.AnswerDataset{Name: req.Dataset.Name, TopK: req.Dataset.TopK}
	for _, ex := range req.Dataset.Examples {
		answerDS.Examples = append(answerDS.Examples, rageval.AnswerExample{Example: ex})
	}
	bench := rageval.AnswerBenchmark{
		Asker:   askerAdapter{port: s.port, namespace: req.Namespace, maxTokens: s.cfg.MaxAskTokens},
		Judge:   rageval.LLMJudge{Model: s.judge},
		Options: ragcore.AskOptions{Search: ragcore.SearchOptions{Namespace: req.Namespace}},
	}
	curr, err := bench.Run(ctx, answerDS)
	if err != nil {
		return EvalResult{}, nil, fmt.Errorf("eval: drift benchmark run: %w", err)
	}
	var prev rageval.BenchmarkResult
	if len(req.PrevBenchmarkJSON) > 0 {
		if err := json.Unmarshal(req.PrevBenchmarkJSON, &prev); err != nil {
			return EvalResult{}, nil, fmt.Errorf("eval: decode prev benchmark: %w", err)
		}
	}
	report := rageval.CompareBenchmarks(prev, curr)
	dv := driftView(report)
	gv := metricsFromGeneration(curr.Metrics.MeanGroundedness, curr.Metrics.MeanAnswerRelevance, curr.Metrics.Examples)
	baseline, err := marshalBaseline(curr)
	if err != nil {
		return EvalResult{}, nil, fmt.Errorf("eval: marshal baseline: %w", err)
	}
	return EvalResult{Kind: KindDrift, DatasetName: req.Dataset.Name, Generation: &gv, Drift: &dv}, baseline, nil
}

// RunDrift is the drift entry point used by the Runner (httpapi path): it
// returns the kb-local result AND the scrubbed current BenchmarkResult JSON to
// persist as the next baseline.
func (s *Service) RunDrift(ctx context.Context, req RunRequest) (EvalResult, []byte, error) {
	return s.runDrift(ctx, req)
}

// marshalBaseline serializes a BenchmarkResult for storage, scrubbing NaN
// (BenchmarkMetrics has no json tags and NaN does not round-trip). NaN→0.
func marshalBaseline(r rageval.BenchmarkResult) ([]byte, error) {
	scrub := func(f float64) float64 {
		if math.IsNaN(f) {
			return 0
		}
		return f
	}
	r.Metrics.ExactMatch = scrub(r.Metrics.ExactMatch)
	r.Metrics.F1Token = scrub(r.Metrics.F1Token)
	r.Metrics.RequiredPhraseRecall = scrub(r.Metrics.RequiredPhraseRecall)
	r.Metrics.ReflectionRoundsMean = scrub(r.Metrics.ReflectionRoundsMean)
	r.Metrics.GraderAdoptionRate = scrub(r.Metrics.GraderAdoptionRate)
	r.Metrics.FollowupQueriesUsedMean = scrub(r.Metrics.FollowupQueriesUsedMean)
	r.Metrics.ActiveRetrievalFireRate = scrub(r.Metrics.ActiveRetrievalFireRate)
	r.Metrics.MeanGroundedness = scrub(r.Metrics.MeanGroundedness)
	r.Metrics.MeanAnswerRelevance = scrub(r.Metrics.MeanAnswerRelevance)
	return json.Marshal(r)
}

// runStore is the eval_run persistence surface the Runner needs (satisfied by
// *Store). Narrowed for unit-testability; ListByKB is included so ListRuns is a
// compile-time guarantee (no runtime type assertion).
type runStore interface {
	Insert(ctx context.Context, in InsertInput) (string, error)
	LatestBenchmark(ctx context.Context, kbID, datasetName string) ([]byte, bool, error)
	ListByKB(ctx context.Context, kbID string, limit int, cursor string) ([]RunRow, string, error)
}

// Runner is the httpapi.EvalRunner implementation: parse JSONL → run → persist.
type Runner struct {
	svc         *Service
	store       runStore
	defaultTopK int
}

// NewRunner composes the eval Service + eval_run store. defaultTopK is the
// fallback Dataset.TopK when the inline JSONL omits top_k.
func NewRunner(svc *Service, store runStore, defaultTopK int) *Runner {
	if defaultTopK <= 0 {
		defaultTopK = 5
	}
	return &Runner{svc: svc, store: store, defaultTopK: defaultTopK}
}

// LoadDataset parses inline JSONL bytes into an eval.Dataset (one Example per
// line; lines beginning with // or # and blank lines skipped). TopK comes from
// the first line that carries top_k, else defaultTopK. Mirrors eval.LoadJSONL
// but reads from memory (the upload is inline, not a file path).
func (rn *Runner) LoadDataset(name string, jsonl []byte) (rageval.Dataset, error) {
	ds := rageval.Dataset{Name: name, TopK: rn.defaultTopK}
	type wire struct {
		rageval.Example
		TopK int `json:"top_k,omitempty"`
	}
	sc := bufio.NewScanner(bytes.NewReader(jsonl))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	topKSet := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "#") {
			continue
		}
		var w wire
		if err := json.Unmarshal([]byte(raw), &w); err != nil {
			return rageval.Dataset{}, fmt.Errorf("eval: dataset line %d: %w", lineNo, err)
		}
		if w.TopK > 0 && !topKSet {
			ds.TopK = w.TopK
			topKSet = true
		}
		ds.Examples = append(ds.Examples, w.Example)
	}
	if err := sc.Err(); err != nil {
		return rageval.Dataset{}, fmt.Errorf("eval: scan dataset: %w", err)
	}
	if len(ds.Examples) == 0 {
		return rageval.Dataset{}, fmt.Errorf("eval: dataset is empty")
	}
	return ds, nil
}

// RunEval parses the dataset, runs the kind, persists the eval_run row, and
// returns the kb-local result + run id. Satisfies httpapi.EvalRunner.
func (rn *Runner) RunEval(ctx context.Context, kbID, namespace string, kind Kind, datasetJSONL []byte) (EvalResult, string, error) {
	ds, err := rn.LoadDataset("inline", datasetJSONL)
	if err != nil {
		return EvalResult{}, "", err
	}
	if kind == KindDrift {
		prev, _, err := rn.store.LatestBenchmark(ctx, kbID, ds.Name)
		if err != nil {
			return EvalResult{}, "", fmt.Errorf("eval: load baseline: %w", err)
		}
		res, baseline, err := rn.svc.RunDrift(ctx, RunRequest{
			KBID: kbID, Namespace: namespace, Kind: kind, Dataset: ds, PrevBenchmarkJSON: prev,
		})
		if err != nil {
			return EvalResult{}, "", err
		}
		driftJSON, err := json.Marshal(res.Drift)
		if err != nil {
			return EvalResult{}, "", fmt.Errorf("eval: marshal drift: %w", err)
		}
		// metrics_json for drift = the scrubbed BenchmarkResult (next baseline).
		id, err := rn.store.Insert(ctx, InsertInput{
			KBID: kbID, Kind: kind, DatasetName: ds.Name,
			MetricsJSON: baseline, DriftJSON: driftJSON,
		})
		if err != nil {
			return EvalResult{}, "", err
		}
		return res, id, nil
	}

	res, err := rn.svc.Run(ctx, RunRequest{KBID: kbID, Namespace: namespace, Kind: kind, Dataset: ds})
	if err != nil {
		return EvalResult{}, "", err
	}
	var metricsJSON []byte
	switch {
	case res.Retrieval != nil:
		metricsJSON, err = json.Marshal(res.Retrieval)
	case res.Generation != nil:
		metricsJSON, err = json.Marshal(res.Generation)
	default:
		metricsJSON = []byte(`{}`)
	}
	if err != nil {
		return EvalResult{}, "", fmt.Errorf("eval: marshal metrics: %w", err)
	}
	id, err := rn.store.Insert(ctx, InsertInput{
		KBID: kbID, Kind: kind, DatasetName: ds.Name, MetricsJSON: metricsJSON,
	})
	if err != nil {
		return EvalResult{}, "", err
	}
	return res, id, nil
}

// ListRuns lists eval runs for a kb (satisfies httpapi.EvalRunner).
func (rn *Runner) ListRuns(ctx context.Context, kbID string, limit int, cursor string) ([]RunRow, string, error) {
	return rn.store.ListByKB(ctx, kbID, limit, cursor)
}
