import { useState } from "react"
import { useAskStream } from "./useAskStream"
import { askGlobal, askDrift } from "./api"
import { streamAsk as realStreamAsk } from "@/lib/sse"
import { AnswerPane } from "./AnswerPane"
import { CitationList } from "./CitationList"
import { DiagnosticsDrawer } from "./DiagnosticsDrawer"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import type { Citation } from "@/lib/types"

type Mode = "vector" | "hybrid" | "global" | "drift"

// AskPage drives streaming for vector/hybrid (token-by-token) and the
// non-stream endpoints for global/drift (no stream endpoint exists). streamAsk
// is injectable for tests.
export function AskPage({
  kbId,
  streamAsk = realStreamAsk,
}: {
  kbId: string
  streamAsk?: typeof realStreamAsk
}) {
  const [mode, setMode] = useState<Mode>("hybrid")
  const [q, setQ] = useState("")
  const stream = useAskStream(kbId, streamAsk)

  // Non-stream (global/drift) state.
  const [nsAnswer, setNsAnswer] = useState("")
  const [nsCitations, setNsCitations] = useState<Citation[]>([])
  const [nsDiagnostics, setNsDiagnostics] = useState<Record<string, unknown> | null>(null)
  const [nsLoading, setNsLoading] = useState(false)

  const streaming = mode === "vector" || mode === "hybrid"

  const onAsk = async () => {
    if (!q) return
    if (streaming) {
      await stream.run(q, mode, 5)
    } else {
      setNsLoading(true)
      setNsAnswer("")
      setNsCitations([])
      setNsDiagnostics(null)
      try {
        const res = mode === "global" ? await askGlobal(kbId, q) : await askDrift(kbId, q)
        setNsAnswer(res.answer)
        setNsCitations(res.citations)
        setNsDiagnostics(res.diagnostics)
      } finally {
        setNsLoading(false)
      }
    }
  }

  const answer = streaming ? stream.answer : nsAnswer
  const citations = streaming ? stream.citations : nsCitations
  const diagnostics = streaming ? stream.diagnostics : nsDiagnostics
  const busy = streaming ? stream.isStreaming : nsLoading

  return (
    <div className="space-y-4">
      <Tabs value={mode} onValueChange={(v) => setMode(v as Mode)}>
        <TabsList>
          <TabsTrigger value="vector">Vector</TabsTrigger>
          <TabsTrigger value="hybrid">Hybrid</TabsTrigger>
          <TabsTrigger value="global">Global</TabsTrigger>
          <TabsTrigger value="drift">Drift</TabsTrigger>
        </TabsList>
      </Tabs>
      <div className="flex gap-2">
        <Input
          placeholder="Ask a question…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void onAsk()
          }}
        />
        <Button onClick={() => void onAsk()} disabled={busy || !q}>
          {busy ? "Asking…" : "Ask"}
        </Button>
      </div>
      {stream.error && streaming && <p className="text-sm text-destructive">{stream.error}</p>}
      <AnswerPane answer={answer} isStreaming={streaming && stream.isStreaming} />
      <DiagnosticsDrawer diagnostics={diagnostics} />
      <CitationList citations={citations} />
    </div>
  )
}
