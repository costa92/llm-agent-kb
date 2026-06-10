import { describe, it, expect, beforeEach, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { AskPage } from "./AskPage"
import type { streamAsk as RealStreamAsk } from "@/lib/sse"
import { setAccessToken } from "@/lib/apiClient"

// streams two tokens then done — asserts the pane shows the concatenation.
const fakeStreamAsk: typeof RealStreamAsk = async (_u, _b, _t, h) => {
  h.onToken("The answer ")
  h.onToken("is 42.")
  h.onDone({
    citations: [{ chunkId: "c1", docId: "d1", title: "Guide", sectionPath: ["Intro"], score: 0.91, snippet: "the snippet" }],
    diagnostics: { mode: "hybrid", hitCount: 1 },
    sessionId: "sid-1",
  })
}

describe("AskPage", () => {
  beforeEach(() => {
    setAccessToken("tok")
    vi.restoreAllMocks()
  })

  it("streams the answer and renders citations for hybrid mode", async () => {
    render(<AskPage kbId="kb-1" streamAsk={fakeStreamAsk} />)
    await userEvent.type(screen.getByPlaceholderText(/ask/i), "what is the answer")
    await userEvent.click(screen.getByRole("button", { name: /^ask$/i }))
    await waitFor(() => expect(screen.getByTestId("answer-pane")).toHaveTextContent("The answer is 42."))
    expect(screen.getByText("Guide")).toBeInTheDocument()
    expect(screen.getByText(/Intro/)).toBeInTheDocument()
    expect(screen.getByText("the snippet")).toBeInTheDocument()
  })
})
