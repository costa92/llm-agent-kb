import { apiJSON } from "@/lib/apiClient"
import type { EvalResult, EvalRunRow, ListEnvelope } from "@/lib/types"

export interface RunEvalResponse {
  runId: string
  result: EvalResult
}

export function runEval(
  kbId: string,
  kind: "retrieval" | "triad" | "global" | "drift",
  dataset: string,
): Promise<RunEvalResponse> {
  return apiJSON<RunEvalResponse>(`/api/kb/${kbId}/eval/run`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind, dataset }),
  })
}

export function listRuns(kbId: string): Promise<ListEnvelope<EvalRunRow>> {
  return apiJSON<ListEnvelope<EvalRunRow>>(`/api/kb/${kbId}/eval/runs`)
}
