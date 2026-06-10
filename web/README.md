# llm-agent-kb web

React 19 + TypeScript SPA for the llm-agent-kb backend (GraphRAG Q&A platform).

## Stack

- Vite 8 + React 19 + TypeScript 6
- Tailwind CSS v4 (via `@tailwindcss/vite`; theme in `src/index.css`, no `tailwind.config.js`)
- shadcn/ui (radix-nova) primitives in `src/components/ui/`
- TanStack Router (file-based, `tsr generate`) + TanStack Query
- SSE via `@microsoft/fetch-event-source` (POST + Bearer) — `src/lib/sse.ts`
- Tests: Vitest + @testing-library/react + jsdom (mocked fetch + SSE; no live backend)

## Develop

```bash
pnpm install
pnpm dev      # tsr generate + vite; proxies /api → http://localhost:8080
```

Start the Go backend (`kbd`) on :8080 separately so the dev proxy can reach it.

## Build & test

```bash
pnpm build    # tsr generate + tsc -b + vite build → dist/
pnpm test     # vitest run (all unit/component tests)
pnpm lint     # eslint
```

## Layout

`src/{app,components,features,lib,routes,test}/`. Features are self-contained dirs
(`api.ts`, page + component `.tsx`, tests). The typed API client (`src/lib/apiClient.ts`)
keeps the access token in memory and refreshes on 401; response types live in
`src/lib/types.ts`. The SSE wire contract (token/done/error) matches the backend
`POST /api/kb/{id}/ask/stream` endpoint.
