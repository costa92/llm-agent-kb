import { describe, it, expect, vi } from "vitest"
import { streamAsk } from "./sse"
import type { StreamDoneData } from "./types"

// A fake fetchEventSource: replays a scripted list of {event,data} frames
// through the provided onmessage, then calls onclose.
function fakeClient(frames: { event: string; data: string }[]) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return vi.fn(async (_url: string, init: any) => {
    await init.onopen?.(new Response(null, { status: 200, headers: { "Content-Type": "text/event-stream" } }))
    for (const f of frames) init.onmessage?.({ id: "", event: f.event, data: f.data })
    init.onclose?.()
  })
}

describe("streamAsk", () => {
  it("forwards token deltas in order and resolves with the done payload", async () => {
    const client = fakeClient([
      { event: "token", data: JSON.stringify({ text: "hello " }) },
      { event: "token", data: JSON.stringify({ text: "world" }) },
      {
        event: "done",
        data: JSON.stringify({
          citations: [{ chunkId: "c1", docId: "d1", title: "Doc", score: 0.9, snippet: "s" }],
          diagnostics: { mode: "hybrid", hitCount: 1 },
          sessionId: "sid-1",
        }),
      },
    ])
    const tokens: string[] = []
    let done: StreamDoneData | undefined
    await streamAsk(
      "/api/kb/demo/ask/stream",
      { q: "fox", mode: "hybrid", topK: 5 },
      "tok",
      { onToken: (t) => tokens.push(t), onDone: (d) => (done = d) },
      client,
    )
    expect(tokens).toEqual(["hello ", "world"])
    expect(done?.sessionId).toBe("sid-1")
    expect(done?.citations[0].chunkId).toBe("c1")
    // the request body + bearer were passed through
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const init = (client.mock.calls[0][1]) as any
    expect(init.method).toBe("POST")
    expect((init.headers as Record<string, string>)["Authorization"]).toBe("Bearer tok")
    expect(JSON.parse(init.body).mode).toBe("hybrid")
  })

  it("invokes onError on an error frame", async () => {
    const client = fakeClient([{ event: "error", data: JSON.stringify({ error: "boom" }) }])
    let err = ""
    await streamAsk(
      "/api/kb/demo/ask/stream",
      { q: "x", mode: "vector" },
      "tok",
      { onToken: () => {}, onDone: () => {}, onError: (m) => (err = m) },
      client,
    )
    expect(err).toBe("boom")
  })
})
