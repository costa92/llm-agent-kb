import { createRootRouteWithContext, Outlet } from "@tanstack/react-router"

interface RouterContext {
  isAuthenticated: boolean
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: () => <Outlet />,
})
