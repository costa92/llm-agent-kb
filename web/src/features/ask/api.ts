import { apiJSON } from "@/lib/apiClient"
import type { AskResponse } from "@/lib/types"

export function askGlobal(kbId: string, q: string, sessionId?: string): Promise<AskResponse> {
  return apiJSON<AskResponse>(`/api/kb/${kbId}/ask/global`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ q, sessionId }),
  })
}

export function askDrift(kbId: string, q: string, sessionId?: string): Promise<AskResponse> {
  return apiJSON<AskResponse>(`/api/kb/${kbId}/ask/drift`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ q, sessionId }),
  })
}
