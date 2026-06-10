import { createFileRoute } from "@tanstack/react-router"

export const Route = createFileRoute("/_authed/")({
  component: () => <div>Knowledge bases</div>,
})
