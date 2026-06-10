import { useState } from "react"
import { Button } from "@/components/ui/button"

export function DiagnosticsDrawer({ diagnostics }: { diagnostics: Record<string, unknown> | null }) {
  const [open, setOpen] = useState(false)
  if (!diagnostics) return null
  return (
    <div>
      <Button variant="ghost" size="sm" onClick={() => setOpen((o) => !o)}>
        {open ? "Hide" : "Show"} diagnostics
      </Button>
      {open && (
        <pre className="mt-2 overflow-x-auto rounded-md border bg-muted p-3 text-xs">
          {JSON.stringify(diagnostics, null, 2)}
        </pre>
      )}
    </div>
  )
}
