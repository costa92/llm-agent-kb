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

export function KbTabs({ kbId }: { kbId: string }) {
  const linkCls = "rounded px-3 py-1.5 text-sm hover:bg-accent"
  return (
    <nav className="flex gap-1 border-b pb-2">
      <Link to="/kb/$kbId/documents" params={{ kbId }} className={linkCls}>
        Documents
      </Link>
      {/* ask/eval/sessions routes land in later tasks; cast until they exist in the route tree. */}
      {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
      <Link {...({ to: "/kb/$kbId/ask", params: { kbId }, className: linkCls } as any)}>Ask</Link>
      {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
      <Link {...({ to: "/kb/$kbId/eval", params: { kbId }, className: linkCls } as any)}>Eval</Link>
      {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
      <Link {...({ to: "/kb/$kbId/sessions", params: { kbId }, className: linkCls } as any)}>Sessions</Link>
    </nav>
  )
}
