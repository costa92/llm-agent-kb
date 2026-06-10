import { describe, it, expect, beforeEach, vi } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { AuthProvider } from "@/app/auth"
import { LoginForm } from "./login"
import { installFetchRoutes, jsonResponse } from "@/test/helpers"
import { setAccessToken } from "@/lib/apiClient"

const navigate = vi.fn()

function renderForm() {
  return render(
    <AuthProvider>
      <LoginForm onSuccess={navigate} />
    </AuthProvider>,
  )
}

describe("LoginForm", () => {
  beforeEach(() => {
    setAccessToken(null)
    navigate.mockReset()
    vi.restoreAllMocks()
  })

  it("logs in and calls onSuccess", async () => {
    installFetchRoutes({ "/api/auth/login": jsonResponse({ access_token: "t", expires_in: 900 }) })
    renderForm()
    await userEvent.type(screen.getByLabelText(/email/i), "a@x.com")
    await userEvent.type(screen.getByLabelText(/password/i), "pw")
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }))
    await waitFor(() => expect(navigate).toHaveBeenCalled())
  })

  it("shows an error on bad credentials", async () => {
    installFetchRoutes({ "/api/auth/login": new Response("nope", { status: 401 }) })
    renderForm()
    await userEvent.type(screen.getByLabelText(/email/i), "a@x.com")
    await userEvent.type(screen.getByLabelText(/password/i), "bad")
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }))
    expect(await screen.findByText(/invalid credentials/i)).toBeInTheDocument()
    expect(navigate).not.toHaveBeenCalled()
  })
})
