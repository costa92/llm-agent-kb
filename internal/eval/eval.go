// Package eval is the kb-level quality use case (§9, §13 M4). It is — alongside
// ragsvc — a permitted importer of rag/eval (spec §4): the rag evaluators are
// struct literals parameterized by rag interfaces, so the glue that builds them
// must see rag types. The OUTWARD boundary is preserved: this package exports
// only kb-local DTOs (EvalResult / MetricsView / GenerationView / DriftView);
// httpapi/retrieval/sessions/cmd-kbd never import rag/eval.
package eval

import (
	"math"

	rageval "github.com/costa92/llm-agent-rag/eval"
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
