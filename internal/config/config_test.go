package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadFromLookup: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr=%q want :8080", cfg.HTTPAddr)
	}
	if cfg.Provider != "ollama" {
		t.Fatalf("Provider=%q want ollama (key-free default)", cfg.Provider)
	}
	if cfg.EmbeddingDim != 768 {
		t.Fatalf("EmbeddingDim=%d want 768", cfg.EmbeddingDim)
	}
	if cfg.MaxAskTokens != 4096 {
		t.Fatalf("MaxAskTokens=%d want 4096", cfg.MaxAskTokens)
	}
	if cfg.MaxRequestsPerUserPerMinute != 30 {
		t.Fatalf("MaxRequestsPerUserPerMinute=%d want 30", cfg.MaxRequestsPerUserPerMinute)
	}
	if cfg.IngestWorkers != 2 {
		t.Fatalf("IngestWorkers=%d want 2", cfg.IngestWorkers)
	}
	if cfg.IngestPollInterval != 2*time.Second {
		t.Fatalf("IngestPollInterval=%v want 2s", cfg.IngestPollInterval)
	}
	if cfg.IngestLease != 60*time.Second {
		t.Fatalf("IngestLease=%v want 60s", cfg.IngestLease)
	}
	if cfg.IngestMaxAttempts != 5 {
		t.Fatalf("IngestMaxAttempts=%d want 5", cfg.IngestMaxAttempts)
	}
	if cfg.IngestBaseBackoff != 5*time.Second {
		t.Fatalf("IngestBaseBackoff=%v want 5s", cfg.IngestBaseBackoff)
	}
	if cfg.MaxUploadBytes != 10<<20 {
		t.Fatalf("MaxUploadBytes=%d want 10MiB", cfg.MaxUploadBytes)
	}
	if cfg.KBStorageQuotaBytes != 256<<20 {
		t.Fatalf("KBStorageQuotaBytes=%d want 256MiB", cfg.KBStorageQuotaBytes)
	}
	if cfg.ParseTimeout != 30*time.Second {
		t.Fatalf("ParseTimeout=%v want 30s", cfg.ParseTimeout)
	}
	if cfg.FetchTimeout != 15*time.Second {
		t.Fatalf("FetchTimeout=%v want 15s", cfg.FetchTimeout)
	}
	if cfg.FetchMaxBytes != 10<<20 {
		t.Fatalf("FetchMaxBytes=%d want 10MiB", cfg.FetchMaxBytes)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"HTTP_ADDR":      ":9000",
		"LLM_PROVIDER":   "openai",
		"PG_URL":         "postgres://x",
		"EMBEDDING_DIM":  "1536",
		"JWT_SECRET":     "supersecret",
		"MAX_ASK_TOKENS": "1000",
	}
	cfg, err := LoadFromLookup(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("LoadFromLookup: %v", err)
	}
	if cfg.HTTPAddr != ":9000" || cfg.Provider != "openai" || cfg.PGURL != "postgres://x" ||
		cfg.EmbeddingDim != 1536 || cfg.JWTSecret != "supersecret" || cfg.MaxAskTokens != 1000 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	_, err := LoadFromLookup(func(k string) (string, bool) {
		if k == "LLM_PROVIDER" {
			return "bogus", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("unknown provider must error")
	}
}

func TestLoadGraphDefaults(t *testing.T) {
	cfg, err := LoadFromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GraphEnabled {
		t.Errorf("GraphEnabled default = false, want true")
	}
	if cfg.LouvainResolution != 1.0 {
		t.Errorf("LouvainResolution = %v, want 1.0", cfg.LouvainResolution)
	}
	if cfg.EntityResolverEnabled {
		t.Errorf("EntityResolverEnabled default = true, want false (opt-in)")
	}
	if cfg.GlobalMaxCommunities != 8 {
		t.Errorf("GlobalMaxCommunities = %d, want 8", cfg.GlobalMaxCommunities)
	}
	if cfg.DriftRounds != 2 {
		t.Errorf("DriftRounds = %d, want 2", cfg.DriftRounds)
	}
}

func TestLoadGraphOverrides(t *testing.T) {
	env := map[string]string{
		"GRAPH_ENABLED":             "false",
		"LOUVAIN_RESOLUTION":        "1.5",
		"ENTITY_RESOLVER_ENABLED":   "true",
		"ENTITY_RESOLVER_THRESHOLD": "0.92",
		"GLOBAL_MAX_COMMUNITIES":    "16",
	}
	cfg, err := LoadFromLookup(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GraphEnabled {
		t.Error("GRAPH_ENABLED=false not honored")
	}
	if cfg.LouvainResolution != 1.5 {
		t.Errorf("LouvainResolution = %v, want 1.5", cfg.LouvainResolution)
	}
	if !cfg.EntityResolverEnabled || cfg.EntityResolverThreshold != 0.92 {
		t.Errorf("resolver override not honored: enabled=%v thr=%v", cfg.EntityResolverEnabled, cfg.EntityResolverThreshold)
	}
	if cfg.GlobalMaxCommunities != 16 {
		t.Errorf("GlobalMaxCommunities = %d, want 16", cfg.GlobalMaxCommunities)
	}
}
