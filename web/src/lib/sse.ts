import { fetchEventSource } from "@microsoft/fetch-event-source"
import type { StreamDoneData, StreamTokenData, StreamErrorData } from "./types"

export interface AskStreamBody {
  q: string
  mode: "vector" | "hybrid"
  topK?: number
  sessionId?: string
}

export interface StreamHandlers {
  onToken: (text: string) => void
  onDone: (done: StreamDoneData) => void
  onError?: (message: string) => void
}

// The injectable client signature (matches fetchEventSource). Tests pass a fake.
type EventSourceClient = (
  url: string,
  init: {
    method: string
    headers: Record<string, string>
    body: string
    signal?: AbortSignal
    openWhenHidden?: boolean
    onopen?: (res: Response) => Promise<void> | void
    onmessage?: (ev: { id: string; event: string; data: string }) => void
    onclose?: () => void
    onerror?: (err: unknown) => number | void
  },
) => Promise<void>

// streamAsk opens the POST SSE stream with the Bearer token and dispatches the
// token/done/error wire frames (M5a contract). Returns when the stream closes.
// `signal` lets callers abort (e.g. component unmount / new question).
export async function streamAsk(
  url: string,
  body: AskStreamBody,
  accessToken: string,
  handlers: StreamHandlers,
  client: EventSourceClient = fetchEventSource as unknown as EventSourceClient,
  signal?: AbortSignal,
): Promise<void> {
  await client(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${accessToken}`,
    },
    body: JSON.stringify(body),
    signal,
    openWhenHidden: true,
    onmessage(ev) {
      switch (ev.event) {
        case "token": {
          const d = JSON.parse(ev.data) as StreamTokenData
          handlers.onToken(d.text)
          break
        }
        case "done": {
          const d = JSON.parse(ev.data) as StreamDoneData
          handlers.onDone(d)
          break
        }
        case "error": {
          const d = JSON.parse(ev.data) as StreamErrorData
          handlers.onError?.(d.error)
          break
        }
      }
    },
  })
}
