import type { DriftView } from "@/lib/types"
import { Badge } from "@/components/ui/badge"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

function directionVariant(d: string): "default" | "secondary" | "destructive" {
  if (d === "improved") return "default"
  if (d === "regressed") return "destructive"
  return "secondary"
}

function fmt(v: number | null): string {
  return v === null ? "—" : v.toFixed(3)
}

export function DriftReportTable({ drift }: { drift: DriftView }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Metric</TableHead>
          <TableHead>Prev</TableHead>
          <TableHead>Curr</TableHead>
          <TableHead>Delta</TableHead>
          <TableHead>Direction</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {drift.deltas.map((d) => (
          <TableRow key={d.name}>
            <TableCell>{d.name}</TableCell>
            <TableCell className="tabular-nums">{fmt(d.prev)}</TableCell>
            <TableCell className="tabular-nums">{fmt(d.curr)}</TableCell>
            <TableCell className="tabular-nums">{fmt(d.delta)}</TableCell>
            <TableCell>
              <Badge variant={directionVariant(d.direction)}>{d.direction}</Badge>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}
