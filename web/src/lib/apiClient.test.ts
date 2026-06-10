import { describe, it, expect, beforeEach, vi } from "vitest"
import { apiFetch, setAccessToken, getAccessToken } from "./apiClient"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("apiFetch", () => {
  beforeEach(() => {
    setAccessToken(null)
    vi.restoreAllMocks()
  })

  it("injects the Authorization header from the in-memory token", async () => {
    setAccessToken("tok-1")
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }))
    vi.stubGlobal("fetch", fetchMock)

    await apiFetch("/api/kb/x")

    const [, init] = fetchMock.mock.calls[0]
    expect((init.headers as Headers).get("Authorization")).toBe("Bearer tok-1")
  })

  it("refreshes once on 401 then retries the original request", async () => {
    setAccessToken("stale")
    const fetchMock = vi
      .fn()
      // 1st: original request → 401
      .mockResolvedValueOnce(new Response("unauthorized", { status: 401 }))
      // 2nd: refresh → new token
      .mockResolvedValueOnce(jsonResponse({ access_token: "fresh", expires_in: 900 }))
      // 3rd: retried original → 200
      .mockResolvedValueOnce(jsonResponse({ ok: true }))
    vi.stubGlobal("fetch", fetchMock)

    const res = await apiFetch("/api/kb/x")
    expect(res.status).toBe(200)
    expect(getAccessToken()).toBe("fresh")
    // refresh call carried the X-CSRF header + credentials
    const refreshInit = fetchMock.mock.calls[1][1]
    expect((refreshInit.headers as Headers).get("X-CSRF")).toBe("1")
    expect(refreshInit.credentials).toBe("include")
    // retried request used the fresh token
    const retryInit = fetchMock.mock.calls[2][1]
    expect((retryInit.headers as Headers).get("Authorization")).toBe("Bearer fresh")
  })

  it("single-flights refresh under concurrent 401s (refresh called once)", async () => {
    setAccessToken("stale")
    let refreshCount = 0
    const fetchMock = vi.fn((input: string) => {
      const url = String(input)
      if (url.endsWith("/api/auth/refresh")) {
        refreshCount++
        return Promise.resolve(jsonResponse({ access_token: "fresh", expires_in: 900 }))
      }
      // first hit per caller is 401 (stale), retry (fresh) is 200
      return Promise.resolve(
        getAccessTokenInternal() === "fresh"
          ? jsonResponse({ ok: true })
          : new Response("unauthorized", { status: 401 }),
      )
    })
    // helper to read the live token inside the mock
    function getAccessTokenInternal() {
      return getAccessToken()
    }
    vi.stubGlobal("fetch", fetchMock)

    const [a, b] = await Promise.all([apiFetch("/api/kb/a"), apiFetch("/api/kb/b")])
    expect(a.status).toBe(200)
    expect(b.status).toBe(200)
    expect(refreshCount).toBe(1)
  })

  it("clears the token and throws when refresh fails", async () => {
    setAccessToken("stale")
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("unauthorized", { status: 401 }))
      .mockResolvedValueOnce(new Response("unauthorized", { status: 401 })) // refresh fails
    vi.stubGlobal("fetch", fetchMock)

    await expect(apiFetch("/api/kb/x")).rejects.toThrow()
    expect(getAccessToken()).toBeNull()
  })
})
