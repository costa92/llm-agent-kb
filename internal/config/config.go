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

// Config is the kbd runtime configuration (M1 fields only).
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
