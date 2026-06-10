import { apiJSON } from "@/lib/apiClient"
import type { Citation, ListEnvelope, SessionRow, TranscriptMessage } from "@/lib/types"

interface TranscriptResponse {
  sessionId: string
  messages: TranscriptMessage[]
}

export function listSessions(kbId: string): Promise<ListEnvelope<SessionRow>> {
  return apiJSON<ListEnvelope<SessionRow>>(`/api/kb/${kbId}/sessions`)
}

export async function getTranscript(kbId: string, sid: string): Promise<TranscriptMessage[]> {
  const res = await apiJSON<TranscriptResponse>(`/api/kb/${kbId}/sessions/${sid}`)
  return res.messages
}

export type { Citation }
