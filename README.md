# llm-agent-kb

Enterprise knowledge-base GraphRAG Q&A platform (backend). M1 delivers: authz-backed login/JWT/RBAC, knowledge-base CRUD, Markdown/TXT/paste ingest (synchronous), vector + hybrid `Ask` with citations, single-document delete with cascade cleanup, basic per-user rate limiting, otel tracing, and docker-compose. PDF/DOCX/URL ingest, async workers, GraphRAG global/drift, eval dashboards, and the React frontend are later milestones.

## Run

```bash
docker compose up --build
# pull models once: docker compose exec ollama ollama pull llama3.1 nomic-embed-text
```

## Architecture

Single Go binary `kbd` embedding a llm-agent-rag System. Layered internal packages: config · storage (pgxpool + business migrations + rag postgres.Store) · ragsvc (the only rag-touching package; RagPort + adapters + otelrag.Wrap) · orgkb · ingest · retrieval · limits · httpapi · obs. Tenancy/auth comes from the imported `llm-agent-authz` library.

## Tests

Pure/unit suites run with `GOWORK=off go test ./internal/config/ ./internal/ragsvc/ ./internal/ingest/ ./internal/retrieval/ ./internal/limits/ ./internal/httpapi/`. Storage/orgkb/cascade/e2e tests need a **pgvector-enabled** Postgres DSN in `LLM_AGENT_KB_PG_URL` (skipped otherwise) — e.g. `pgvector/pgvector:pg16`, NOT vanilla postgres.

## Note

Standalone sibling repo; run Go commands with `GOWORK=off` (the umbrella `go.work` does not list kb). The ecosystem replace-guard pre-commit hook strips local `replace` directives and pins published tags on commit.
