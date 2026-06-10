import { Link, Outlet } from "@tanstack/react-router"
import { useAuth } from "./auth"
import { Button } from "@/components/ui/button"

// AppShell is the nav frame for authenticated routes. kbId-scoped links are
// rendered by the per-kb pages; the shell carries the top-level nav + logout.
export function AppShell() {
  const { logout } = useAuth()
  return (
    <div className="min-h-screen">
      <header className="flex items-center justify-between border-b px-6 py-3">
        <nav className="flex items-center gap-4">
          <Link to="/" className="font-semibold">
            llm-agent-kb
          </Link>
        </nav>
        <Button variant="outline" size="sm" onClick={() => void logout()}>
          Sign out
        </Button>
      </header>
      <main className="p-6">
        <Outlet />
      </main>
    </div>
  )
}
