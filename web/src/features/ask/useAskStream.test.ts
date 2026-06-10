import { describe, it, expect } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"
import { useAskStream } from "./useAskStream"
import type { streamAsk as RealStreamAsk } from "@/lib/sse"
import { setAccessToken } from "@/lib/apiClient"

// fakeStreamAsk replays tokens then done through the handlers synchronously.
const fakeStreamAsk: typeof RealStreamAsk = async (_url, _body, _tok, handlers) => {
  handlers.onToken("hello ")
  handlers.onToken("world")
  handlers.onDone({
    citations: [{ chunkId: "c1", docId: "d1", title: "Doc", score: 0.9, snippet: "snip" }],
    diagnostics: { mode: "hybrid", hitCount: 1 },
    sessionId: "sid-1",
  })
}

describe("useAskStream", () => {
  it("accumulates tokens and captures the done payload", async () => {
    setAccessToken("tok")
    const { result } = renderHook(() => useAskStream("kb-1", fakeStreamAsk))
    await act(async () => {
      await result.current.run("fox", "hybrid", 5)
    })
    await waitFor(() => expect(result.current.answer).toBe("hello world"))
    expect(result.current.citations[0].chunkId).toBe("c1")
    expect(result.current.diagnostics?.mode).toBe("hybrid")
    expect(result.current.sessionId).toBe("sid-1")
    expect(result.current.isStreaming).toBe(false)
  })

  it("surfaces an error frame", async () => {
    setAccessToken("tok")
    const erroring: typeof RealStreamAsk = async (_u, _b, _t, h) => {
      h.onError?.("boom")
    }
    const { result } = renderHook(() => useAskStream("kb-1", erroring))
    await act(async () => {
      await result.current.run("x", "vector")
    })
    await waitFor(() => expect(result.current.error).toBe("boom"))
  })
})
