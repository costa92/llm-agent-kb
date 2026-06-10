import { describe, it, expect, beforeEach, vi } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"
import { AuthProvider, useAuth } from "./auth"
import { getAccessToken, setAccessToken } from "@/lib/apiClient"
import { installFetchRoutes, jsonResponse } from "@/test/helpers"

const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>

describe("AuthProvider", () => {
  beforeEach(() => {
    setAccessToken(null)
    vi.restoreAllMocks()
  })

  it("starts unauthenticated", () => {
    installFetchRoutes({})
    const { result } = renderHook(() => useAuth(), { wrapper })
    expect(result.current.isAuthenticated).toBe(false)
  })

  it("login stores the access token and flips isAuthenticated", async () => {
    installFetchRoutes({
      "/api/auth/login": jsonResponse({ access_token: "tok-9", expires_in: 900 }),
    })
    const { result } = renderHook(() => useAuth(), { wrapper })
    await act(async () => {
      await result.current.login("a@x.com", "pw")
    })
    await waitFor(() => expect(result.current.isAuthenticated).toBe(true))
    expect(getAccessToken()).toBe("tok-9")
  })

  it("login throws on bad credentials and stays unauthenticated", async () => {
    installFetchRoutes({
      "/api/auth/login": new Response("invalid credentials", { status: 401 }),
    })
    const { result } = renderHook(() => useAuth(), { wrapper })
    await expect(
      act(async () => {
        await result.current.login("a@x.com", "bad")
      }),
    ).rejects.toThrow()
    expect(result.current.isAuthenticated).toBe(false)
  })

  it("logout clears the token", async () => {
    installFetchRoutes({
      "/api/auth/login": jsonResponse({ access_token: "tok-9", expires_in: 900 }),
      "/api/auth/logout": new Response(null, { status: 204 }),
    })
    const { result } = renderHook(() => useAuth(), { wrapper })
    await act(async () => {
      await result.current.login("a@x.com", "pw")
    })
    await act(async () => {
      await result.current.logout()
    })
    expect(getAccessToken()).toBeNull()
    expect(result.current.isAuthenticated).toBe(false)
  })
})
