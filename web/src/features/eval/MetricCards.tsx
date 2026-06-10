import type { EvalResult } from "@/lib/types"
import { Card } from "@/components/ui/card"

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <Card className="p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-2xl font-semibold tabular-nums">{value.toFixed(3)}</div>
    </Card>
  )
}

// MetricCards renders whichever metric leg the EvalResult carries: retrieval
// (precision/recall/MRR/grounding) and/or the generation triad means.
export function MetricCards({ result }: { result: EvalResult }) {
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
      {result.retrieval && (
        <>
          <Metric label="Precision@K" value={result.retrieval.precisionAtK} />
          <Metric label="Recall@K" value={result.retrieval.recallAtK} />
          <Metric label="MRR" value={result.retrieval.mrr} />
          <Metric label="Grounding@K" value={result.retrieval.groundingAtK} />
        </>
      )}
      {result.generation && (
        <>
          <Metric label="Mean Groundedness" value={result.generation.meanGroundedness} />
          <Metric label="Mean Answer Relevance" value={result.generation.meanAnswerRelevance} />
        </>
      )}
    </div>
  )
}
