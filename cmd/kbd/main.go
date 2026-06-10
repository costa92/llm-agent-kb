// Command kbd is the llm-agent-kb backend server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"

	authzhttp "github.com/costa92/llm-agent-authz/httpapi"
	authzsvc "github.com/costa92/llm-agent-authz/service"
	authzstore "github.com/costa92/llm-agent-authz/store"
	authztoken "github.com/costa92/llm-agent-authz/token"
	"github.com/costa92/llm-agent-contract/llm"
	ollamaprovider "github.com/costa92/llm-agent-providers/ollama"
	openaiprovider "github.com/costa92/llm-agent-providers/openai"

	"github.com/costa92/llm-agent-kb/internal/config"
	kbeval "github.com/costa92/llm-agent-kb/internal/eval"
	"github.com/costa92/llm-agent-kb/internal/fetch"
	"github.com/costa92/llm-agent-kb/internal/httpapi"
	"github.com/costa92/llm-agent-kb/internal/ingest"
	"github.com/costa92/llm-agent-kb/internal/obs"
	"github.com/costa92/llm-agent-kb/internal/orgkb"
	"github.com/costa92/llm-agent-kb/internal/ragsvc"
	"github.com/costa92/llm-agent-kb/internal/retrieval"
	"github.com/costa92/llm-agent-kb/internal/sessions"
	"github.com/costa92/llm-agent-kb/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("kbd: config: %v", err)
	}
	ctx := context.Background()

	app, cleanup, err := build(ctx, cfg)
	if err != nil {
		log.Fatalf("kbd: build: %v", err)
	}
	defer cleanup()

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: app}

	// Listen for SIGINT/SIGTERM and drain in flight requests before exit.
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		shutCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("kbd: shutdown: %v", err)
		}
	}()

	log.Printf("kbd: listening on %s", cfg.HTTPAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("kbd: serve: %v", err)
	}
	log.Printf("kbd: stopped")
}

// build wires every dependency and returns the root handler + a cleanup func.
// Exported-shape (lowercase but package-visible) so main_test.go can drive it.
func build(ctx context.Context, cfg config.Config) (http.Handler, func(), error) {
	tp, err := obs.NewTracerProvider(ctx, obs.Config{
		ServiceName: cfg.ServiceName, Endpoint: cfg.OTLPEndpoint,
		Protocol: cfg.OTLPProtocol, Insecure: cfg.OTLPInsecure,
	})
	if err != nil {
		return nil, nil, err
	}

	st, err := storage.Open(ctx, storage.Config{PGURL: cfg.PGURL, EmbeddingDim: cfg.EmbeddingDim})
	if err != nil {
		return nil, nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, nil, err
	}

	az := authzstore.New(st.Pool())
	if err := az.Migrate(ctx); err != nil {
		st.Close()
		return nil, nil, err
	}

	model, embedder, err := buildProviders(cfg)
	if err != nil {
		st.Close()
		return nil, nil, err
	}

	var graphComps ragsvc.GraphComponents
	if cfg.GraphEnabled {
		graphComps = ragsvc.NewLLMGraphComponents(model, embedder, ragsvc.GraphConfig{
			LouvainResolution: cfg.LouvainResolution,
			ResolverEnabled:   cfg.EntityResolverEnabled,
			ResolverThreshold: cfg.EntityResolverThreshold,
		})
	}
	rag := ragsvc.New(ragsvc.Deps{
		Model: model, Embedder: embedder,
		RagStore: st.RagStore(), ChunkStore: st.RagStore(),
		Tracer:              tp,
		EntityExtractor:     graphComps.EntityExtractor,
		EntityResolver:      graphComps.EntityResolver,
		CommunityDetector:   graphComps.CommunityDetector,
		CommunitySummarizer: graphComps.CommunitySummarizer,
	})

	kbRepo := orgkb.New(st.Pool(), az)
	ingestSvc := ingest.New(st.Pool(), rag)
	retrievalSvc := retrieval.New(rag, retrieval.Config{
		MaxAskTokens:         cfg.MaxAskTokens,
		GlobalMaxCommunities: cfg.GlobalMaxCommunities,
		DriftRounds:          cfg.DriftRounds,
		DriftTopK:            cfg.DriftTopK,
	})

	// M4: Q&A history + eval. The session repo backs both the ask-path recorder
	// and the read endpoints; the eval Runner composes the eval Service (over the
	// same RagPort) + the eval_run store.
	sessionRepo := sessions.New(st.Pool())
	retrievalSvc.SetRecorder(sessionRepo)

	evalSvc := kbeval.NewService(rag, rag.JudgeModel(), kbeval.ServiceConfig{
		MaxAskTokens:         cfg.MaxAskTokens,
		GlobalMaxCommunities: cfg.GlobalMaxCommunities,
		DriftRounds:          cfg.DriftRounds,
		DriftTopK:            cfg.DriftTopK,
	})
	evalRunner := kbeval.NewRunner(evalSvc, kbeval.NewStore(st.Pool()), cfg.EvalDefaultTopK)

	fetcher := fetch.New(fetch.Config{
		Timeout:             cfg.FetchTimeout,
		MaxBytes:            cfg.FetchMaxBytes,
		AllowedContentTypes: []string{"text/html", "application/xhtml+xml", "text/plain"},
	})

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < cfg.IngestWorkers; i++ {
		w := ingest.NewWorker(ingest.WorkerConfig{
			Pool: st.Pool(), Rag: rag, Fetcher: fetcher,
			WorkerID:     fmt.Sprintf("kbd-%d", i),
			Lease:        cfg.IngestLease,
			MaxAttempts:  cfg.IngestMaxAttempts,
			BaseBackoff:  cfg.IngestBaseBackoff,
			ParseTimeout: cfg.ParseTimeout,
		})
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(workerCtx, cfg.IngestPollInterval) }()
	}

	issuer := authztoken.NewIssuer([]byte(cfg.JWTSecret), cfg.AccessTTL)
	authService := authzsvc.New(az, issuer, cfg.RefreshTTL)
	authHandlers := authzhttp.New(authService)

	mux := httpapi.NewMux(httpapi.Deps{
		Issuer:       issuer,
		AuthHandlers: authHandlers,
		RoleResolver: az, // *store.Store satisfies authzhttp.RoleResolver
		OrgLookup:    kbRepo,
		Asker:        retrievalSvc,
		Community:    rag, // *ragsvc.Service satisfies httpapi.CommunityReader
		Ingester:     ingestSvc,
		KBRepo:       kbRepo,
		PerUserLimit: cfg.MaxRequestsPerUserPerMinute,

		MaxUploadBytes:      cfg.MaxUploadBytes,
		KBStorageQuotaBytes: cfg.KBStorageQuotaBytes,
		DocStatus:           ingestSvc,

		EvalRunner:            evalRunner,
		SessionReader:         sessionRepo,
		EvalRunsPerUserMinute: cfg.MaxEvalRunsPerUserPerMinute,
	})

	cleanup := func() {
		stopWorkers() // signal workers to stop claiming
		wg.Wait()     // drain in-flight jobs (lease protects un-drained ones)
		_ = tp.Shutdown(ctx)
		st.Close()
	}
	return mux, cleanup, nil
}

// providerOverride lets tests inject scripted models instead of real providers.
var providerOverride func(config.Config) (llm.ChatModel, llm.Embedder, error)

func buildProviders(cfg config.Config) (llm.ChatModel, llm.Embedder, error) {
	if providerOverride != nil {
		return providerOverride(cfg)
	}
	chat, err := buildChat(cfg)
	if err != nil {
		return nil, nil, err
	}
	emb, err := buildEmbedder(cfg)
	if err != nil {
		return nil, nil, err
	}
	return chat, emb, nil
}

func buildChat(cfg config.Config) (llm.ChatModel, error) {
	switch cfg.Provider {
	case config.ProviderOpenAI:
		return openaiprovider.New(
			openaiprovider.WithModel(cfg.Model),
			openaiprovider.WithAPIKey(cfg.OpenAIAPIKey),
			openaiprovider.WithBaseURL(cfg.OpenAIBaseURL),
		)
	default: // ollama
		opts := []ollamaprovider.Option{ollamaprovider.WithModel(cfg.Model)}
		if cfg.OllamaBaseURL != "" {
			opts = append(opts, ollamaprovider.WithBaseURL(cfg.OllamaBaseURL))
		}
		return ollamaprovider.New(opts...)
	}
}

func buildEmbedder(cfg config.Config) (llm.Embedder, error) {
	switch cfg.EmbeddingProvider {
	case config.ProviderOpenAI:
		return openaiprovider.New(
			openaiprovider.WithModel(cfg.EmbeddingModel),
			openaiprovider.WithAPIKey(cfg.OpenAIAPIKey),
			openaiprovider.WithBaseURL(cfg.OpenAIBaseURL),
		)
	default: // ollama
		opts := []ollamaprovider.Option{ollamaprovider.WithModel(cfg.EmbeddingModel)}
		if cfg.OllamaBaseURL != "" {
			opts = append(opts, ollamaprovider.WithBaseURL(cfg.OllamaBaseURL))
		}
		return ollamaprovider.New(opts...)
	}
}
