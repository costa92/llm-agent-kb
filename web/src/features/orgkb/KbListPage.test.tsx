import { describe, it, expect, beforeEach, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from "@tanstack/react-router"
import { KbListPage } from "./KbListPage"
import { installFetchRoutes, jsonResponse } from "@/test/helpers"
import { setAccessToken } from "@/lib/apiClient"

// KbListPage renders a <Link> for each kb row, which requires a RouterProvider
// context (TanStack Router 1.170.x throws otherwise). We mount the page inside
// a minimal in-memory router that registers the /kb/$kbId/documents target so
// the link resolves, then assert the page's own output.
function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => <KbListPage initialOrgId="org-1" />,
  })
  const docsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/kb/$kbId/documents",
    component: () => <div>documents</div>,
  })
  const routeTree = rootRoute.addChildren([indexRoute, docsRoute])
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  }) as any
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe("KbListPage", () => {
  beforeEach(() => {
    setAccessToken("tok")
    vi.restoreAllMocks()
  })

  it("lists the kbs for the selected org", async () => {
    installFetchRoutes({
      "/api/orgs/org-1/kbs": jsonResponse({
        items: [
          { id: "kb-1", orgId: "org-1", name: "Handbook", namespace: "kb_kb-1" },
          { id: "kb-2", orgId: "org-1", name: "Runbooks", namespace: "kb_kb-2" },
        ],
        next_cursor: "",
      }),
    })
    renderPage()
    expect(await screen.findByText("Handbook")).toBeInTheDocument()
    expect(screen.getByText("Runbooks")).toBeInTheDocument()
  })

  it("shows an empty state when the org has no kbs", async () => {
    installFetchRoutes({
      "/api/orgs/org-1/kbs": jsonResponse({ items: [], next_cursor: "" }),
    })
    renderPage()
    await waitFor(() => expect(screen.getByText(/no knowledge bases/i)).toBeInTheDocument())
  })
})
