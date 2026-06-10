import { createFileRoute } from "@tanstack/react-router"

export const Route = createFileRoute("/")({
  component: () => <div className="p-6 text-xl font-bold">llm-agent-kb</div>,
})
