// Package config loads kbd configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
)

// Config is the kbd runtime configuration (M1 + M2 fields).
type Config struct {
	HTTPAddr string

	PGURL string // Postgres DSN; pgvector-enabled.

	Provider          string // chat model provider: ollama|openai
	Model             string // chat model name
	EmbeddingProvider string // embedding provider: ollama|openai
	EmbeddingModel    string // embedding model name
	EmbeddingDim      int    // vector dimension; must match the embedding model and the chunks table

	OpenAIAPIKey  string
	OpenAIBaseURL string
	OllamaBaseURL string

	JWTSecret  string        // HS256 secret for authz token.Issuer
	AccessTTL  time.Duration // access token TTL
	RefreshTTL time.Duration // refresh session TTL

	MaxAskTokens                int // per-Ask cumulative token budget (rag AskOptions.MaxTotalTokens)
	MaxRequestsPerUserPerMinute int // per-user fixed-window cap on ask/upload

	// M2 ingest worker pool.
	IngestWorkers      int           // number of concurrent worker goroutines
	IngestPollInterval time.Duration // queue poll interval when idle
	IngestLease        time.Duration // job lease (locked_until = now + lease); stuck jobs reclaimed past it
	IngestMaxAttempts  int           // attempts before a job becomes 'dead'
	IngestBaseBackoff  time.Duration // base for exponential backoff (next_run_at = now + base*2^attempts)

	// M2 upload / parse safety (§16.3).
	MaxUploadBytes      int64         // http.MaxBytesReader cap per document body
	KBStorageQuotaBytes int64         // per-kb cumulative byte quota (sum of document content sizes)
	ParseTimeout        time.Duration // context deadline around PDF/DOCX parse (anti parse-bomb)

	// M2 SSRF-safe URL fetch (§16.3).
	FetchTimeout  time.Duration // connect+read deadline for outbound URL ingest
	FetchMaxBytes int64         // max response body bytes for outbound URL ingest

	// M3 GraphRAG (§13 M3, §6 step 4, §7).
	GraphEnabled            bool    // wire EntityExtractor/Louvain/Summarizer into rag.New (default true)
	LouvainResolution       float64 // graph.LouvainDetector.Resolution; <=0 → 1.0
	EntityResolverEnabled   bool    // opt-in near-dup entity merge (graph.EmbeddingEntityResolver)
	EntityResolverThreshold float64 // resolver cosine-similarity threshold; <=0 → rag default
	GlobalMaxCommunities    int     // GlobalOptions.MaxCommunities default (also DriftOptions.MaxCommunities)
	DriftRounds             int     // DriftOptions.Rounds default
	DriftTopK               int     // DriftOptions.TopK default

	ServiceName  string
	OTLPEndpoint string
	OTLPProtocol string
	OTLPInsecure bool

	ShutdownTimeout time.Duration
}

// Load reads from the process environment.
func Load() (Config, error) { return LoadFromLookup(os.LookupEnv) }

// LoadFromLookup builds a Config from an injectable env lookup (testable).
func LoadFromLookup(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		HTTPAddr:                    envOr(lookup, "HTTP_ADDR", ":8080"),
		PGURL:                       envOr(lookup, "PG_URL", ""),
		Provider:                    strings.ToLower(envOr(lookup, "LLM_PROVIDER", ProviderOllama)),
		Model:                       envOr(lookup, "LLM_MODEL", ""),
		EmbeddingProvider:           strings.ToLower(envOr(lookup, "EMBEDDING_PROVIDER", "")),
		EmbeddingModel:              envOr(lookup, "EMBEDDING_MODEL", ""),
		EmbeddingDim:                envInt(lookup, "EMBEDDING_DIM", 768),
		OpenAIAPIKey:                envOr(lookup, "OPENAI_API_KEY", ""),
		OpenAIBaseURL:               envOr(lookup, "OPENAI_BASE_URL", ""),
		OllamaBaseURL:               envOr(lookup, "OLLAMA_HOST", ""),
		JWTSecret:                   envOr(lookup, "JWT_SECRET", "dev-insecure-secret-change-me"),
		AccessTTL:                   time.Duration(envInt(lookup, "ACCESS_TTL_MINUTES", 15)) * time.Minute,
		RefreshTTL:                  time.Duration(envInt(lookup, "REFRESH_TTL_HOURS", 720)) * time.Hour,
		MaxAskTokens:                envInt(lookup, "MAX_ASK_TOKENS", 4096),
		MaxRequestsPerUserPerMinute: envInt(lookup, "MAX_REQUESTS_PER_USER_PER_MINUTE", 30),
		IngestWorkers:               envInt(lookup, "INGEST_WORKERS", 2),
		IngestPollInterval:          time.Duration(envInt(lookup, "INGEST_POLL_INTERVAL_SECONDS", 2)) * time.Second,
		IngestLease:                 time.Duration(envInt(lookup, "INGEST_LEASE_SECONDS", 60)) * time.Second,
		IngestMaxAttempts:           envInt(lookup, "INGEST_MAX_ATTEMPTS", 5),
		IngestBaseBackoff:           time.Duration(envInt(lookup, "INGEST_BASE_BACKOFF_SECONDS", 5)) * time.Second,
		MaxUploadBytes:              int64(envInt(lookup, "MAX_UPLOAD_BYTES", 10<<20)),
		KBStorageQuotaBytes:         int64(envInt(lookup, "KB_STORAGE_QUOTA_BYTES", 256<<20)),
		ParseTimeout:                time.Duration(envInt(lookup, "PARSE_TIMEOUT_SECONDS", 30)) * time.Second,
		FetchTimeout:                time.Duration(envInt(lookup, "FETCH_TIMEOUT_SECONDS", 15)) * time.Second,
		FetchMaxBytes:               int64(envInt(lookup, "FETCH_MAX_BYTES", 10<<20)),
		GraphEnabled:                envBool(lookup, "GRAPH_ENABLED", true),
		LouvainResolution:           envFloat(lookup, "LOUVAIN_RESOLUTION", 1.0),
		EntityResolverEnabled:       envBool(lookup, "ENTITY_RESOLVER_ENABLED", false),
		EntityResolverThreshold:     envFloat(lookup, "ENTITY_RESOLVER_THRESHOLD", 0),
		GlobalMaxCommunities:        envInt(lookup, "GLOBAL_MAX_COMMUNITIES", 8),
		DriftRounds:                 envInt(lookup, "DRIFT_ROUNDS", 2),
		DriftTopK:                   envInt(lookup, "DRIFT_TOP_K", 5),
		ServiceName:                 envOr(lookup, "OTEL_SERVICE_NAME", "llm-agent-kb"),
		OTLPEndpoint:                envOr(lookup, "OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
		OTLPProtocol:                strings.ToLower(envOr(lookup, "OTEL_EXPORTER_OTLP_PROTOCOL", "http")),
		OTLPInsecure:                envBool(lookup, "OTEL_EXPORTER_OTLP_INSECURE", true),
		ShutdownTimeout:             time.Duration(envInt(lookup, "SHUTDOWN_TIMEOUT_SECONDS", 10)) * time.Second,
	}
	if cfg.Provider != ProviderOllama && cfg.Provider != ProviderOpenAI {
		return Config{}, fmt.Errorf("config: unsupported LLM_PROVIDER %q", cfg.Provider)
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel(cfg.Provider)
	}
	if cfg.EmbeddingProvider == "" {
		cfg.EmbeddingProvider = cfg.Provider
	}
	if cfg.EmbeddingProvider != ProviderOllama && cfg.EmbeddingProvider != ProviderOpenAI {
		return Config{}, fmt.Errorf("config: unsupported EMBEDDING_PROVIDER %q", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingModel == "" {
		cfg.EmbeddingModel = defaultEmbeddingModel(cfg.EmbeddingProvider)
	}
	return cfg, nil
}

func defaultModel(p string) string {
	if p == ProviderOpenAI {
		return "gpt-4o-mini"
	}
	return "llama3.1"
}

func defaultEmbeddingModel(p string) string {
	if p == ProviderOpenAI {
		return "text-embedding-3-small"
	}
	return "nomic-embed-text"
}

func envOr(lookup func(string) (string, bool), key, def string) string {
	if v, ok := lookup(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(lookup func(string) (string, bool), key string, def int) int {
	if v, ok := lookup(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func envBool(lookup func(string) (string, bool), key string, def bool) bool {
	if v, ok := lookup(key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

func envFloat(lookup func(string) (string, bool), key string, def float64) float64 {
	if v, ok := lookup(key); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}
