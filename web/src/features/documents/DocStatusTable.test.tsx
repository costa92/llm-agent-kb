import { describe, it, expect, beforeEach, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { DocStatusTable } from "./DocStatusTable"
import { installFetchRoutes, jsonResponse } from "@/test/helpers"
import { setAccessToken } from "@/lib/apiClient"

function renderTable() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <DocStatusTable kbId="kb-1" subscribeProgress={() => () => {}} />
    </QueryClientProvider>,
  )
}

describe("DocStatusTable", () => {
  beforeEach(() => {
    setAccessToken("tok")
    vi.restoreAllMocks()
  })

  it("renders documents with their status", async () => {
    installFetchRoutes({
      "/api/kb/kb-1/documents": jsonResponse({
        items: [
          { id: "d1", title: "Ready Doc", sourceType: "paste", status: "ready", phase: "done", chunkCount: 4 },
          { id: "d2", title: "Failed Doc", sourceType: "url", status: "failed", phase: "parse", chunkCount: 0, error: "boom" },
        ],
        next_cursor: "",
      }),
    })
    renderTable()
    expect(await screen.findByText("Ready Doc")).toBeInTheDocument()
    expect(screen.getByText("ready")).toBeInTheDocument()
    expect(screen.getByText("failed")).toBeInTheDocument()
  })

  it("shows a Retry button for failed docs and posts retry", async () => {
    let retried = false
    installFetchRoutes({
      "/api/kb/kb-1/documents/d2/retry": () => {
        retried = true
        return jsonResponse({ documentId: "d2", status: "pending" }, 202)
      },
      "/api/kb/kb-1/documents": jsonResponse({
        items: [{ id: "d2", title: "Failed Doc", sourceType: "url", status: "failed", phase: "parse", chunkCount: 0, error: "boom" }],
        next_cursor: "",
      }),
    })
    renderTable()
    await screen.findByText("Failed Doc")
    await userEvent.click(screen.getByRole("button", { name: /retry/i }))
    await waitFor(() => expect(retried).toBe(true))
  })
})
