import { apiFetch, apiJSON } from "@/lib/apiClient"
import type { DocumentView, ListEnvelope } from "@/lib/types"

export interface UploadInput {
  title: string
  sourceType: "paste" | "url" | "pdf" | "docx"
  content?: string
  url?: string
  filename?: string
}

export interface UploadAccepted {
  documentId: string
  status: string
}

export function uploadDocument(kbId: string, input: UploadInput): Promise<UploadAccepted> {
  return apiJSON<UploadAccepted>(`/api/kb/${kbId}/documents`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  })
}

export function listDocuments(kbId: string): Promise<ListEnvelope<DocumentView>> {
  return apiJSON<ListEnvelope<DocumentView>>(`/api/kb/${kbId}/documents`)
}

export function retryDocument(kbId: string, docId: string): Promise<UploadAccepted> {
  return apiJSON<UploadAccepted>(`/api/kb/${kbId}/documents/${docId}/retry`, { method: "POST" })
}

export async function deleteDocument(kbId: string, docId: string): Promise<void> {
  const res = await apiFetch(`/api/kb/${kbId}/documents/${docId}`, { method: "DELETE" })
  if (!res.ok && res.status !== 204) throw new Error(`delete failed: ${res.status}`)
}
