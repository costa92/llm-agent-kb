import { createFileRoute, redirect } from "@tanstack/react-router"
import { AppShell } from "@/app/AppShell"

export const Route = createFileRoute("/_authed")({
  beforeLoad: ({ context }) => {
    if (!context.isAuthenticated) {
      throw redirect({ to: "/login" })
    }
  },
  component: AppShell,
})
