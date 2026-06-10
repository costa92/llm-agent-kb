import { createFileRoute } from "@tanstack/react-router"
import { SessionsPage } from "@/features/sessions/SessionsPage"
import { KbTabs } from "@/app/AppShell"

// eslint-disable-next-line react-refresh/only-export-components
function SessionsRoute() {
  const { kbId } = Route.useParams()
  return (
    <div className="space-y-6">
      <KbTabs kbId={kbId} />
      <SessionsPage kbId={kbId} />
    </div>
  )
}

export const Route = createFileRoute("/_authed/kb/$kbId/sessions")({ component: SessionsRoute })
