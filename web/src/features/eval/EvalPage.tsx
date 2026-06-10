import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { listRuns, type RunEvalResponse } from "./api"
import { MetricCards } from "./MetricCards"
import { DriftReportTable } from "./DriftReportTable"
import { RunEvalDialog } from "./RunEvalDialog"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

export function EvalPage({ kbId }: { kbId: string }) {
  const [latest, setLatest] = useState<RunEvalResponse | null>(null)
  const runsQuery = useQuery({ queryKey: ["eval-runs", kbId], queryFn: () => listRuns(kbId) })

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Evaluation</h1>
        <RunEvalDialog kbId={kbId} onComplete={setLatest} />
      </div>

      {latest && (
        <div className="space-y-4">
          <MetricCards result={latest.result} />
          {latest.result.drift && <DriftReportTable drift={latest.result.drift} />}
        </div>
      )}

      <div>
        <h2 className="mb-2 text-sm font-medium text-muted-foreground">Past runs</h2>
        {runsQuery.data && runsQuery.data.items.length === 0 && (
          <p className="text-sm text-muted-foreground">No runs yet.</p>
        )}
        {runsQuery.data && runsQuery.data.items.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Kind</TableHead>
                <TableHead>Dataset</TableHead>
                <TableHead>Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runsQuery.data.items.map((r) => (
                <TableRow key={r.id}>
                  <TableCell>{r.kind}</TableCell>
                  <TableCell>{r.datasetName}</TableCell>
                  <TableCell className="text-xs">{r.createdAt}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  )
}
