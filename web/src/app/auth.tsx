import { createContext, useContext, useState, useCallback, type ReactNode } from "react"
import { apiFetch, setAccessToken } from "@/lib/apiClient"
import type { LoginResponse } from "@/lib/types"

interface AuthValue {
  isAuthenticated: boolean
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false)

  // login uses the PascalCase {Email,Password} body the Go authz handler decodes.
  const login = useCallback(async (email: string, password: string) => {
    const res = await fetch("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ Email: email, Password: password }),
    })
    if (!res.ok) throw new Error("invalid credentials")
    const body = (await res.json()) as LoginResponse
    setAccessToken(body.access_token)
    setIsAuthenticated(true)
  }, [])

  const logout = useCallback(async () => {
    try {
      await apiFetch("/api/auth/logout", {
        method: "POST",
        headers: new Headers({ "X-CSRF": "1" }),
      })
    } finally {
      setAccessToken(null)
      setIsAuthenticated(false)
    }
  }, [])

  return <AuthContext.Provider value={{ isAuthenticated, login, logout }}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error("useAuth must be used within AuthProvider")
  return ctx
}
