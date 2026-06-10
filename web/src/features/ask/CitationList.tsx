import type { Citation } from "@/lib/types"

export function CitationList({ citations }: { citations: Citation[] }) {
  if (citations.length === 0) return null
  return (
    <ul className="space-y-2">
      {citations.map((c) => (
        <li key={c.chunkId} className="rounded-md border p-3 text-sm">
          <div className="font-medium">{c.title}</div>
          {c.sectionPath && c.sectionPath.length > 0 && (
            <div className="text-xs text-muted-foreground">{c.sectionPath.join(" › ")}</div>
          )}
          <p className="mt-1 text-muted-foreground">{c.snippet}</p>
          <div className="mt-1 text-xs">score {c.score.toFixed(3)}</div>
        </li>
      ))}
    </ul>
  )
}
