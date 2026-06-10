package eval

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	rageval "github.com/costa92/llm-agent-rag/eval"
	raggenerate "github.com/costa92/llm-agent-rag/generate"
	ragcore "github.com/costa92/llm-agent-rag/rag"
	ragstore "github.com/costa92/llm-agent-rag/store"

	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

// stubJudgeModel returns a fixed JSON judgement via the rag generate.Model seam.
type stubJudgeModel struct{}

func (stubJudgeModel) Generate(ctx context.Context, req raggenerate.Request) (raggenerate.Response, error) {
	return raggenerate.Response{Text: `{"groundedness":1.0,"answer_relevance":1.0}`}, nil
}

// fakePort implements just the RagPort methods the adapters call.
type fakePort struct {
	hits   []ragstore.Hit
	answer ragcore.Answer
}

func (f fakePort) Retrieve(ctx context.Context, q string, opts ragcore.SearchOptions) ([]ragstore.Hit, error) {
	return f.hits, nil
}
func (f fakePort) Ask(ctx context.Context, q string, req ragsvc.AskRequest) (ragcore.Answer, error) {
	return f.answer, nil
}
func (f fakePort) AskGlobal(ctx context.Context, q string, req ragsvc.GlobalRequest) (ragcore.Answer, error) {
	return f.answer, nil
}
func (f fakePort) AskDrift(ctx context.Context, q string, req ragsvc.DriftRequest) (ragcore.Answer, error) {
	return f.answer, nil
}

func TestRetrieverAdapterMapsOptions(t *testing.T) {
	port := fakePort{hits: []ragstore.Hit{{}, {}}}
	r := retrieverAdapter{port: port, namespace: "kb_x"}
	hits, err := r.Retrieve(context.Background(), "q", ragcore.SearchOptions{TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
}

func TestAskerAdapterReturnsAnswer(t *testing.T) {
	port := fakePort{answer: ragcore.Answer{Text: "hi"}}
	a := askerAdapter{port: port, namespace: "kb_x", maxTokens: 100}
	ans, err := a.Ask(context.Background(), "q", ragcore.AskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "hi" {
		t.Fatalf("answer = %q", ans.Text)
	}
}

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

func TestRunRetrieval(t *testing.T) {
	port := fakePort{hits: []ragstore.Hit{{}}}
	svc := NewService(port, stubJudgeModel{}, ServiceConfig{MaxAskTokens: 100, GlobalMaxCommunities: 4, DriftRounds: 1, DriftTopK: 3})
	ds := rageval.Dataset{Name: "ds", TopK: 5, Examples: []rageval.Example{{Query: "q1", GoldDocIDs: []string{"d1"}}}}
	res, err := svc.Run(context.Background(), RunRequest{KBID: "kb1", Namespace: "kb_kb1", Kind: KindRetrieval, Dataset: ds})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindRetrieval || res.Retrieval == nil {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Retrieval.Examples != 1 {
		t.Fatalf("examples = %d, want 1", res.Retrieval.Examples)
	}
}

func TestRunTriad(t *testing.T) {
	port := fakePort{answer: ragcore.Answer{Text: "a"}}
	svc := NewService(port, stubJudgeModel{}, ServiceConfig{MaxAskTokens: 100})
	ds := rageval.Dataset{Name: "ds", TopK: 5, Examples: []rageval.Example{{Query: "q1"}}}
	res, err := svc.Run(context.Background(), RunRequest{KBID: "kb1", Namespace: "kb_kb1", Kind: KindTriad, Dataset: ds})
	if err != nil {
		t.Fatal(err)
	}
	if res.Generation == nil || res.Generation.MeanGroundedness != 1.0 {
		t.Fatalf("triad generation: %+v", res.Generation)
	}
}

func TestRunRejectsUnknownKind(t *testing.T) {
	svc := NewService(fakePort{}, stubJudgeModel{}, ServiceConfig{})
	_, err := svc.Run(context.Background(), RunRequest{KBID: "kb1", Namespace: "kb_kb1", Kind: Kind("bogus"), Dataset: rageval.Dataset{TopK: 5}})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestMarshalBaselineScrubsNaN(t *testing.T) {
	curr := rageval.BenchmarkResult{
		Dataset: rageval.AnswerDataset{Name: "ds", TopK: 5},
		Metrics: rageval.BenchmarkMetrics{Examples: 1, MeanGroundedness: 0.8, MeanAnswerRelevance: 0.9, ExactMatch: math.NaN(), F1Token: math.NaN()},
	}
	raw, err := marshalBaseline(curr)
	if err != nil {
		t.Fatal(err)
	}
	// Must re-decode cleanly (NaN scrubbed to 0).
	var back rageval.BenchmarkResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("baseline must round-trip: %v (raw=%s)", err, raw)
	}
	if back.Metrics.MeanGroundedness != 0.8 {
		t.Fatalf("groundedness lost: %+v", back.Metrics)
	}
}

// fakeStore captures Insert + serves a baseline.
type fakeStore struct {
	inserted   InsertInput
	insertedID string
	baseline   []byte
}

func (f *fakeStore) Insert(ctx context.Context, in InsertInput) (string, error) {
	f.inserted = in
	f.insertedID = "run-1"
	return f.insertedID, nil
}
func (f *fakeStore) LatestBenchmark(ctx context.Context, kbID, ds string) ([]byte, bool, error) {
	if f.baseline == nil {
		return nil, false, nil
	}
	return f.baseline, true, nil
}
func (f *fakeStore) ListByKB(ctx context.Context, kbID string, limit int, cursor string) ([]RunRow, string, error) {
	return nil, "", nil
}

func TestRunnerRunEvalRetrieval(t *testing.T) {
	port := fakePort{hits: []ragstore.Hit{{}}}
	svc := NewService(port, stubJudgeModel{}, ServiceConfig{})
	store := &fakeStore{}
	runner := NewRunner(svc, store, 5)
	jsonl := `{"query":"q1","gold_doc_ids":["d1"],"top_k":5}`
	res, id, err := runner.RunEval(context.Background(), "kb1", "kb_kb1", KindRetrieval, []byte(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if id != "run-1" {
		t.Fatalf("id = %q", id)
	}
	if res.Retrieval == nil {
		t.Fatalf("no retrieval metrics: %+v", res)
	}
	if store.inserted.Kind != KindRetrieval || store.inserted.KBID != "kb1" {
		t.Fatalf("inserted = %+v", store.inserted)
	}
	if store.inserted.DriftJSON != nil {
		t.Fatalf("retrieval run must not set drift_json")
	}
}

func TestRunnerRunEvalDriftPersistsBaseline(t *testing.T) {
	port := fakePort{answer: ragcore.Answer{Text: "a"}}
	svc := NewService(port, stubJudgeModel{}, ServiceConfig{})
	store := &fakeStore{}
	runner := NewRunner(svc, store, 5)
	jsonl := `{"query":"q1","top_k":5}`
	res, _, err := runner.RunEval(context.Background(), "kb1", "kb_kb1", KindDrift, []byte(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if res.Drift == nil {
		t.Fatalf("no drift view: %+v", res)
	}
	// metrics_json for drift carries the scrubbed benchmark (must re-decode).
	var bench rageval.BenchmarkResult
	if err := json.Unmarshal(store.inserted.MetricsJSON, &bench); err != nil {
		t.Fatalf("drift metrics_json must be a decodable BenchmarkResult: %v (raw=%s)", err, store.inserted.MetricsJSON)
	}
	if store.inserted.DriftJSON == nil {
		t.Fatalf("drift run must set drift_json")
	}
}

func TestRunnerRejectsEmptyDataset(t *testing.T) {
	runner := NewRunner(NewService(fakePort{}, stubJudgeModel{}, ServiceConfig{}), &fakeStore{}, 5)
	_, _, err := runner.RunEval(context.Background(), "kb1", "kb_kb1", KindRetrieval, []byte("\n\n"))
	if err == nil {
		t.Fatal("empty dataset must error")
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
