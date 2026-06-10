import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { listSessions, getTranscript } from "./api"
import { CitationList } from "@/features/ask/CitationList"
import { Button } from "@/components/ui/button"

export function SessionsPage({ kbId }: { kbId: string }) {
  const [activeSid, setActiveSid] = useState<string | null>(null)
  const sessionsQuery = useQuery({ queryKey: ["sessions", kbId], queryFn: () => listSessions(kbId) })
  const transcriptQuery = useQuery({
    queryKey: ["transcript", kbId, activeSid],
    queryFn: () => getTranscript(kbId, activeSid!),
    enabled: !!activeSid,
  })

  return (
    <div className="grid grid-cols-3 gap-6">
      <div className="space-y-1">
        <h2 className="mb-2 text-sm font-medium text-muted-foreground">Sessions</h2>
        {sessionsQuery.data?.items.length === 0 && (
          <p className="text-sm text-muted-foreground">No sessions yet.</p>
        )}
        {sessionsQuery.data?.items.map((s) => (
          <Button
            key={s.id}
            variant={activeSid === s.id ? "secondary" : "ghost"}
            className="w-full justify-start"
            onClick={() => setActiveSid(s.id)}
          >
            {s.title || s.id}
          </Button>
        ))}
      </div>
      <div className="col-span-2 space-y-4">
        {!activeSid && <p className="text-sm text-muted-foreground">Select a session.</p>}
        {transcriptQuery.data?.map((m) => (
          <div key={m.id} className="rounded-lg border p-3">
            <div className="mb-1 text-xs font-medium uppercase text-muted-foreground">{m.role}</div>
            <div className="whitespace-pre-wrap text-sm">{m.content}</div>
            {m.citations && m.citations.length > 0 && (
              <div className="mt-2">
                <CitationList citations={m.citations} />
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
