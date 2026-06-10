import { describe, it, expect } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import {
  RouterProvider,
  createRouter,
  createRootRouteWithContext,
  createRoute,
  createMemoryHistory,
  redirect,
  Outlet,
} from "@tanstack/react-router"
import { AuthProvider } from "@/app/auth"
import { AppShell } from "@/app/AppShell"

interface RouterContext {
  isAuthenticated: boolean
}

// Build a minimal in-memory router that wires the same beforeLoad guard and
// AppShell component the file-based /_authed route uses, so we can drive the
// guard with an injected context. (The file route binds to the generated root,
// so we reconstruct an equivalent tree here.)
function buildRouter(isAuthenticated: boolean) {
  const rootRoute = createRootRouteWithContext<RouterContext>()({
    component: () => <Outlet />,
  })
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/login",
    component: () => <div>login page</div>,
  })
  const protectedRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: "_authed",
    beforeLoad: ({ context }) => {
      if (!context.isAuthenticated) {
        throw redirect({ to: "/login" })
      }
    },
    component: AppShell,
  })
  const indexRoute = createRoute({
    getParentRoute: () => protectedRoute,
    path: "/",
    component: () => <div>Knowledge bases</div>,
  })
  const routeTree = rootRoute.addChildren([
    loginRoute,
    protectedRoute.addChildren([indexRoute]),
  ])
  return createRouter({
    routeTree,
    context: { isAuthenticated },
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
}

function renderRouter(isAuthenticated: boolean) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const router = buildRouter(isAuthenticated) as any
  render(
    <AuthProvider>
      <RouterProvider router={router} />
    </AuthProvider>,
  )
  return router
}

describe("_authed protected layout", () => {
  it("redirects unauthenticated users to /login", async () => {
    const router = renderRouter(false)
    await waitFor(() => expect(router.state.location.pathname).toBe("/login"))
    expect(screen.getByText(/login page/i)).toBeInTheDocument()
  })

  it("renders the app shell for authenticated users", async () => {
    renderRouter(true)
    expect(await screen.findByRole("link", { name: /llm-agent-kb/i })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /sign out/i })).toBeInTheDocument()
    expect(screen.getByText(/knowledge bases/i)).toBeInTheDocument()
  })
})
