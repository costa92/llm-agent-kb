package config

import "testing"

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
