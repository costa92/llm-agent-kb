package eval

import (
	"encoding/json"
	"math"
	"testing"

	rageval "github.com/costa92/llm-agent-rag/eval"
)

func TestRetrievalMetricsView(t *testing.T) {
	m := rageval.Metrics{PrecisionAtK: 0.5, RecallAtK: 0.25, MRR: 0.75, GroundingAtK: 1.0, Examples: 4, TopK: 5}
	v := metricsFromRetrieval(m)
	if v.PrecisionAtK != 0.5 || v.RecallAtK != 0.25 || v.MRR != 0.75 || v.GroundingAtK != 1.0 {
		t.Fatalf("unexpected view: %+v", v)
	}
	if v.Examples != 4 || v.TopK != 5 {
		t.Fatalf("examples/topk: %+v", v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"precisionAtK":0.5`; !contains(string(b), want) {
		t.Fatalf("json %s missing %s", b, want)
	}
}

func TestGenerationMetricsView(t *testing.T) {
	v := metricsFromGeneration(0.8, 0.9, 3)
	if v.MeanGroundedness != 0.8 || v.MeanAnswerRelevance != 0.9 || v.Examples != 3 {
		t.Fatalf("unexpected view: %+v", v)
	}
}

func TestDriftViewNaNBecomesNull(t *testing.T) {
	report := rageval.DriftReport{
		Dataset: "ds",
		Deltas: []rageval.MetricDelta{
			{Name: "MeanGroundedness", Prev: 0.5, Curr: 0.7, Delta: 0.2, Direction: rageval.DirectionImproved},
			{Name: "ExactMatch", Prev: math.NaN(), Curr: math.NaN(), Delta: math.NaN(), Direction: rageval.DirectionUndefined},
		},
		NewExamples: []string{"q2"},
		Histograms: []rageval.HistogramDelta{
			{Name: "AdoptedRoundCounts", Prev: []int{1, 0}, Curr: []int{0, 1}, Delta: []int{-1, 1}, L1Distance: 2},
		},
	}
	v := driftView(report)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal drift view (NaN must not break json): %v", err)
	}
	s := string(b)
	if !contains(s, `"direction":"improved"`) {
		t.Fatalf("drift json missing improved direction: %s", s)
	}
	if !contains(s, `"delta":null`) {
		t.Fatalf("NaN delta must serialize as null: %s", s)
	}
	if !contains(s, `"newExamples":["q2"]`) {
		t.Fatalf("drift json missing newExamples: %s", s)
	}
	if len(v.Histograms) != 1 || v.Histograms[0].Name != "AdoptedRoundCounts" || v.Histograms[0].L1Distance != 2 {
		t.Fatalf("histogram projection missing: %+v", v.Histograms)
	}
	if !contains(s, `"histograms":[`) || !contains(s, `"l1Distance":2`) {
		t.Fatalf("drift json missing histograms projection: %s", s)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
