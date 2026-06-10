import { createFileRoute } from "@tanstack/react-router"
import { EvalPage } from "@/features/eval/EvalPage"
import { KbTabs } from "@/app/AppShell"

// eslint-disable-next-line react-refresh/only-export-components
function EvalRoute() {
  const { kbId } = Route.useParams()
  return (
    <div className="space-y-6">
      <KbTabs kbId={kbId} />
      <EvalPage kbId={kbId} />
    </div>
  )
}

export const Route = createFileRoute("/_authed/kb/$kbId/eval")({ component: EvalRoute })
