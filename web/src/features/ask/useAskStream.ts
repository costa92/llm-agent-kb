import { useCallback, useRef, useState } from "react"
import { streamAsk as realStreamAsk } from "@/lib/sse"
import { getAccessToken } from "@/lib/apiClient"
import type { Citation, StreamDoneData } from "@/lib/types"

interface AskStreamState {
  answer: string
  citations: Citation[]
  diagnostics: StreamDoneData["diagnostics"] | null
  sessionId: string | null
  isStreaming: boolean
  error: string | null
  run: (q: string, mode: "vector" | "hybrid", topK?: number) => Promise<void>
}

// useAskStream drives POST /ask/stream and accumulates the streamed answer.
// streamAsk is injectable (default = the real SSE helper) for unit testing.
export function useAskStream(
  kbId: string,
  streamAsk: typeof realStreamAsk = realStreamAsk,
): AskStreamState {
  const [answer, setAnswer] = useState("")
  const [citations, setCitations] = useState<Citation[]>([])
  const [diagnostics, setDiagnostics] = useState<StreamDoneData["diagnostics"] | null>(null)
  const [sessionId, setSessionId] = useState<string | null>(null)
  const [isStreaming, setIsStreaming] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const run = useCallback(
    async (q: string, mode: "vector" | "hybrid", topK?: number) => {
      abortRef.current?.abort()
      const ctrl = new AbortController()
      abortRef.current = ctrl
      setAnswer("")
      setCitations([])
      setDiagnostics(null)
      setSessionId(null)
      setError(null)
      setIsStreaming(true)
      try {
        await streamAsk(
          `/api/kb/${kbId}/ask/stream`,
          { q, mode, topK },
          getAccessToken() ?? "",
          {
            onToken: (t) => setAnswer((prev) => prev + t),
            onDone: (d) => {
              setCitations(d.citations)
              setDiagnostics(d.diagnostics)
              setSessionId(d.sessionId)
            },
            onError: (m) => setError(m),
          },
          undefined,
          ctrl.signal,
        )
      } finally {
        setIsStreaming(false)
      }
    },
    [kbId, streamAsk],
  )

  return { answer, citations, diagnostics, sessionId, isStreaming, error, run }
}
