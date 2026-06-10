import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { RouterProvider, createRouter } from "@tanstack/react-router"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { routeTree } from "./routeTree.gen"
import { AuthProvider, useAuth } from "./app/auth"
import "./index.css"

const queryClient = new QueryClient()

const router = createRouter({
  routeTree,
  context: { isAuthenticated: false },
})
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}

// eslint-disable-next-line react-refresh/only-export-components
function InnerApp() {
  const auth = useAuth()
  return <RouterProvider router={router} context={{ isAuthenticated: auth.isAuthenticated }} />
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AuthProvider>
      <QueryClientProvider client={queryClient}>
        <InnerApp />
      </QueryClientProvider>
    </AuthProvider>
  </StrictMode>,
)
