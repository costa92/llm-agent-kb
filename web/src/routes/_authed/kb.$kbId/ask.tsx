import { createFileRoute } from "@tanstack/react-router"
import { AskPage } from "@/features/ask/AskPage"
import { KbTabs } from "@/app/AppShell"

// eslint-disable-next-line react-refresh/only-export-components
function AskRoute() {
  const { kbId } = Route.useParams()
  return (
    <div className="space-y-6">
      <KbTabs kbId={kbId} />
      <AskPage kbId={kbId} />
    </div>
  )
}

export const Route = createFileRoute("/_authed/kb/$kbId/ask")({ component: AskRoute })
