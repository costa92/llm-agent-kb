import { vi } from "vitest"

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

// installFetchRoutes stubs global fetch with a path→response map. Each value is
// either a Response or a function (path, init) => Response, called per request.
type RouteValue = Response | ((path: string, init?: RequestInit) => Response)
export function installFetchRoutes(routes: Record<string, RouteValue>) {
  const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    for (const [pattern, value] of Object.entries(routes)) {
      if (path.includes(pattern)) {
        const r = typeof value === "function" ? value(path, init) : value
        return Promise.resolve(r)
      }
    }
    return Promise.resolve(new Response("no route", { status: 404 }))
  })
  vi.stubGlobal("fetch", mock)
  return mock
}
