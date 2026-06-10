import { describe, it, expect, beforeEach, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { UploadForm } from "./UploadForm"
import { installFetchRoutes, jsonResponse } from "@/test/helpers"
import { setAccessToken } from "@/lib/apiClient"

function renderForm(onUploaded = vi.fn()) {
  const qc = new QueryClient()
  render(
    <QueryClientProvider client={qc}>
      <UploadForm kbId="kb-1" onUploaded={onUploaded} />
    </QueryClientProvider>,
  )
  return onUploaded
}

describe("UploadForm", () => {
  beforeEach(() => {
    setAccessToken("tok")
    vi.restoreAllMocks()
  })

  it("submits a paste document and calls onUploaded", async () => {
    const calls: { body: unknown }[] = []
    installFetchRoutes({
      "/api/kb/kb-1/documents": (_p, init) => {
        calls.push({ body: JSON.parse(String(init?.body)) })
        return jsonResponse({ documentId: "doc-9", status: "pending" }, 202)
      },
    })
    const onUploaded = renderForm()
    await userEvent.type(screen.getByLabelText(/title/i), "My Note")
    await userEvent.type(screen.getByLabelText(/content/i), "hello body")
    await userEvent.click(screen.getByRole("button", { name: /upload/i }))
    await waitFor(() => expect(onUploaded).toHaveBeenCalled())
    expect(calls[0].body).toMatchObject({ title: "My Note", sourceType: "paste", content: "hello body" })
  })
})
