// Mirrors the Go backend wire shapes (verified against the kb source).

export interface ListEnvelope<T> {
  items: T[]
  next_cursor: string
}

export interface LoginResponse {
  access_token: string
  expires_in: number
}

export interface Org {
  id: string
  name: string
}

export interface Kb {
  id: string
  orgId: string
  name: string
  namespace: string
}

export interface DocumentView {
  id: string
  title: string
  sourceType: string
  status: string
  phase: string
  error?: string
  chunkCount: number
}

export interface Citation {
  chunkId: string
  docId: string
  title: string
  sectionPath?: string[]
  score: number
  snippet: string
}

export interface AskResponse {
  answer: string
  citations: Citation[]
  diagnostics: Record<string, unknown>
  sessionId?: string
}

// SSE stream frame payloads (POST /ask/stream wire contract from M5a).
export interface StreamTokenData {
  text: string
}
export interface StreamDoneData {
  citations: Citation[]
  diagnostics: { mode: string; hitCount: number }
  sessionId: string
}
export interface StreamErrorData {
  error: string
}

export interface SessionRow {
  id: string
  title: string
  createdAt: string
}
export interface TranscriptMessage {
  id: string
  role: string
  content: string
  mode: string
  createdAt: string
  citations?: Citation[]
}

export interface RetrievalMetrics {
  precisionAtK: number
  recallAtK: number
  mrr: number
  groundingAtK: number
  examples: number
  topK: number
}
export interface GenerationMetrics {
  meanGroundedness: number
  meanAnswerRelevance: number
  examples: number
}
export type DriftDirection = "improved" | "regressed" | "unchanged"
export interface MetricDelta {
  name: string
  prev: number | null
  curr: number | null
  delta: number | null
  direction: DriftDirection
}
export interface DriftView {
  dataset: string
  deltas: MetricDelta[]
  histograms: {
    name: string
    prev: number[]
    curr: number[]
    delta: number[]
    l1Distance: number
  }[]
  newExamples: string[]
  droppedExamples: string[]
}
export interface EvalResult {
  kind: "retrieval" | "triad" | "global" | "drift"
  datasetName: string
  retrieval?: RetrievalMetrics
  generation?: GenerationMetrics
  drift?: DriftView
}
export interface EvalRunRow {
  id: string
  kind: string
  datasetName: string
  createdAt: string
  metrics: Record<string, unknown>
  drift?: DriftView
}
