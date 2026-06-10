// In-memory access token (never persisted to localStorage). The refresh token
// lives in an httpOnly cookie set by /api/auth/login and is sent automatically.
let accessToken: string | null = null
let refreshInFlight: Promise<string> | null = null

export function setAccessToken(t: string | null): void {
  accessToken = t
}
export function getAccessToken(): string | null {
  return accessToken
}

export class AuthError extends Error {}

// refresh POSTs /api/auth/refresh (cookie-driven). The backend requires the
// X-CSRF double-submit header and the httpOnly cookie (credentials:include).
// Single-flight: concurrent callers share one in-flight refresh promise.
async function refresh(): Promise<string> {
  if (refreshInFlight) return refreshInFlight
  refreshInFlight = (async () => {
    try {
      const res = await fetch("/api/auth/refresh", {
        method: "POST",
        headers: new Headers({ "X-CSRF": "1" }),
        credentials: "include",
      })
      if (!res.ok) throw new AuthError("refresh failed")
      const body = (await res.json()) as { access_token: string }
      setAccessToken(body.access_token)
      return body.access_token
    } finally {
      refreshInFlight = null
    }
  })()
  return refreshInFlight
}

function withAuth(init: RequestInit | undefined, token: string | null): RequestInit {
  const headers = new Headers(init?.headers)
  if (token) headers.set("Authorization", `Bearer ${token}`)
  return { ...init, headers, credentials: "include" }
}

// apiFetch injects the bearer token and transparently refreshes once on a 401,
// then retries the original request a single time. A failed refresh clears the
// token and throws AuthError (callers redirect to /login).
export async function apiFetch(path: string, init?: RequestInit): Promise<Response> {
  let res = await fetch(path, withAuth(init, accessToken))
  if (res.status !== 401) return res
  let fresh: string
  try {
    fresh = await refresh()
  } catch (e) {
    setAccessToken(null)
    throw e instanceof AuthError ? e : new AuthError(String(e))
  }
  res = await fetch(path, withAuth(init, fresh))
  return res
}

// apiJSON is the typed convenience wrapper used by feature api modules.
export async function apiJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await apiFetch(path, init)
  if (!res.ok) {
    const text = await res.text().catch(() => "")
    throw new Error(`${res.status} ${path}: ${text || res.statusText}`)
  }
  return (await res.json()) as T
}
