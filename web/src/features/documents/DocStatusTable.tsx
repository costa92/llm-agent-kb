import { useEffect } from "react"
import { fetchEventSource } from "@microsoft/fetch-event-source"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { listDocuments, retryDocument, deleteDocument } from "./api"
import { getAccessToken } from "@/lib/apiClient"
import type { DocumentView } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

// SubscribeProgress opens the document progress SSE and calls onUpdate per
// frame; returns an unsubscribe. Injectable so tests pass a no-op.
export type SubscribeProgress = (
  kbId: string,
  docId: string,
  onUpdate: () => void,
) => () => void

const defaultSubscribe: SubscribeProgress = (kbId, docId, onUpdate) => {
  const ctrl = new AbortController()
  void fetchEventSource(`/api/kb/${kbId}/documents/${docId}/progress`, {
    method: "GET",
    headers: { Authorization: `Bearer ${getAccessToken() ?? ""}` },
    signal: ctrl.signal,
    openWhenHidden: true,
    onmessage() {
      onUpdate()
    },
  })
  return () => ctrl.abort()
}

function statusVariant(status: string): "default" | "secondary" | "destructive" {
  if (status === "ready") return "default"
  if (status === "failed") return "destructive"
  return "secondary"
}

export function DocStatusTable({
  kbId,
  subscribeProgress = defaultSubscribe,
}: {
  kbId: string
  subscribeProgress?: SubscribeProgress
}) {
  const qc = useQueryClient()
  const docsQuery = useQuery({
    queryKey: ["documents", kbId],
    queryFn: () => listDocuments(kbId),
  })

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["documents", kbId] })

  const retryMut = useMutation({
    mutationFn: (docId: string) => retryDocument(kbId, docId),
    onSuccess: invalidate,
  })
  const deleteMut = useMutation({
    mutationFn: (docId: string) => deleteDocument(kbId, docId),
    onSuccess: invalidate,
  })

  // Subscribe to live progress for any non-terminal document; refetch on update.
  const items: DocumentView[] = docsQuery.data?.items ?? []
  useEffect(() => {
    const unsubs = items
      .filter((d) => d.status !== "ready" && d.status !== "failed")
      .map((d) => subscribeProgress(kbId, d.id, invalidate))
    return () => unsubs.forEach((u) => u())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kbId, items.map((d) => `${d.id}:${d.status}`).join(",")])

  if (docsQuery.isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (items.length === 0) return <p className="text-sm text-muted-foreground">No documents yet.</p>

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Title</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Chunks</TableHead>
          <TableHead className="text-right">Actions</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.map((d) => (
          <TableRow key={d.id}>
            <TableCell>{d.title}</TableCell>
            <TableCell>
              <Badge variant={statusVariant(d.status)}>{d.status}</Badge>
            </TableCell>
            <TableCell>{d.chunkCount}</TableCell>
            <TableCell className="space-x-2 text-right">
              {d.status === "failed" && (
                <Button size="sm" variant="outline" onClick={() => retryMut.mutate(d.id)}>
                  Retry
                </Button>
              )}
              <Button size="sm" variant="ghost" onClick={() => deleteMut.mutate(d.id)}>
                Delete
              </Button>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}
