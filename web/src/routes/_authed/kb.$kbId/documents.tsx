import { createFileRoute } from "@tanstack/react-router"
import { useQueryClient } from "@tanstack/react-query"
import { UploadForm } from "@/features/documents/UploadForm"
import { DocStatusTable } from "@/features/documents/DocStatusTable"
import { KbTabs } from "@/app/AppShell"

// eslint-disable-next-line react-refresh/only-export-components
function DocumentsPage() {
  const { kbId } = Route.useParams()
  const qc = useQueryClient()
  return (
    <div className="space-y-6">
      <KbTabs kbId={kbId} />
      <UploadForm kbId={kbId} onUploaded={() => void qc.invalidateQueries({ queryKey: ["documents", kbId] })} />
      <DocStatusTable kbId={kbId} />
    </div>
  )
}

export const Route = createFileRoute("/_authed/kb/$kbId/documents")({ component: DocumentsPage })
