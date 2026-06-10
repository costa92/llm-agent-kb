import { describe, it, expect, beforeEach, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { SessionsPage } from "./SessionsPage"
import { installFetchRoutes, jsonResponse } from "@/test/helpers"
import { setAccessToken } from "@/lib/apiClient"

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SessionsPage kbId="kb-1" />
    </QueryClientProvider>,
  )
}

describe("SessionsPage", () => {
  beforeEach(() => {
    setAccessToken("tok")
    vi.restoreAllMocks()
  })

  it("lists sessions and loads a transcript on click", async () => {
    installFetchRoutes({
      "/api/kb/kb-1/sessions/s1": jsonResponse({
        sessionId: "s1",
        messages: [
          { id: "m1", role: "user", content: "hi", mode: "hybrid", createdAt: "2026-06-10" },
          { id: "m2", role: "assistant", content: "hello there", mode: "hybrid", createdAt: "2026-06-10" },
        ],
      }),
      "/api/kb/kb-1/sessions": jsonResponse({
        items: [{ id: "s1", title: "First chat", createdAt: "2026-06-10" }],
        next_cursor: "",
      }),
    })
    renderPage()
    await userEvent.click(await screen.findByText("First chat"))
    await waitFor(() => expect(screen.getByText("hello there")).toBeInTheDocument())
  })
})
