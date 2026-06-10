import { createFileRoute } from "@tanstack/react-router"
import { KbListPage } from "@/features/orgkb/KbListPage"

export const Route = createFileRoute("/_authed/")({
  component: () => <KbListPage />,
})
