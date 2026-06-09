package ingest

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ragingest "github.com/costa92/llm-agent-rag/ingest"

	"github.com/costa92/llm-agent-kb/internal/fetch"
	"github.com/costa92/llm-agent-kb/internal/ragsvc"
)

// WorkerConfig configures a Worker.
type WorkerConfig struct {
	Pool         *pgxpool.Pool
	Rag          ragsvc.RagPort
	Fetcher      *fetch.Fetcher // for url sources; may be nil if url ingest disabled
	WorkerID     string
	Lease        time.Duration
	MaxAttempts  int
	BaseBackoff  time.Duration
	ParseTimeout time.Duration
	Clock        func() time.Time // injected for deterministic tests; nil → time.Now
	Logger       *slog.Logger     // nil → slog.Default()
}

// Worker drains the ingest_job queue.
type Worker struct {
	cfg WorkerConfig
}

// NewWorker builds a Worker.
func NewWorker(cfg WorkerConfig) *Worker {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{cfg: cfg}
}

// claimed describes a job atomically claimed for processing.
type claimed struct {
	jobID      string
	docID      string
	attempts   int
	kbID       string
	namespace  string
	title      string
	sourceType SourceType
	sourceRef  string
	checksum   string
	raw        []byte
}

// RunOnce claims (if any due/stuck job exists) and processes exactly one job.
// Returns (true, nil) if a job was claimed+processed, (false, nil) if the queue
// is empty. Deterministic for tests (no sleeps).
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	c, ok, err := w.claim(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	w.process(ctx, c)
	return true, nil
}

// Run loops RunOnce, sleeping pollInterval when the queue is empty, until ctx is
// canceled (graceful drain). Production entrypoint (cmd/kbd).
func (w *Worker) Run(ctx context.Context, pollInterval time.Duration) {
	for {
		if ctx.Err() != nil {
			return
		}
		claimed, err := w.RunOnce(ctx)
		if err != nil {
			// transient DB error — log it (otherwise a persistent claim failure
			// is an invisible hot-idle) and back off one poll interval.
			w.cfg.Logger.Error("ingest worker claim failed", "worker", w.cfg.WorkerID, "err", err)
			claimed = false
		}
		if !claimed {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
			}
		}
	}
}

// claim atomically selects one claimable job (pending+due OR running with an
// expired lease) using FOR UPDATE SKIP LOCKED, marks it running with a fresh
// lease, bumps attempts, and loads the document fields needed to process it.
// The lease (locked_until) and the staleness comparison use the DB clock
// (now()) so concurrent workers cannot double-claim; backoff scheduling on
// failure uses the injected clock.
func (w *Worker) claim(ctx context.Context) (claimed, bool, error) {
	tx, err := w.cfg.Pool.Begin(ctx)
	if err != nil {
		return claimed{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lease := int(w.cfg.Lease / time.Second)
	if lease <= 0 {
		lease = 60
	}
	var jobID, docID string
	var attempts int
	row := tx.QueryRow(ctx, `
		SELECT id, document_id, attempts FROM ingest_job
		WHERE (state='pending' AND next_run_at <= now())
		   OR (state='running' AND locked_until IS NOT NULL AND locked_until < now())
		ORDER BY next_run_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1`)
	if err := row.Scan(&jobID, &docID, &attempts); err != nil {
		if err == pgx.ErrNoRows {
			return claimed{}, false, nil
		}
		return claimed{}, false, err
	}
	attempts++
	if _, err := tx.Exec(ctx, `
		UPDATE ingest_job
		SET state='running', locked_by=$2, locked_until = now() + make_interval(secs => $3),
		    attempts=$4, phase='parsing', updated_at=now()
		WHERE id=$1`, jobID, w.cfg.WorkerID, lease, attempts); err != nil {
		return claimed{}, false, err
	}
	c := claimed{jobID: jobID, docID: docID, attempts: attempts}
	if err := tx.QueryRow(ctx, `
		SELECT d.kb_id, kb.namespace, d.title, d.source_type, d.source_ref, d.checksum, d.content_bytes
		FROM document d JOIN knowledge_base kb ON kb.id=d.kb_id WHERE d.id=$1`, docID).
		Scan(&c.kbID, &c.namespace, &c.title, (*string)(&c.sourceType), &c.sourceRef, &c.checksum, new(int64)); err != nil {
		return claimed{}, false, err
	}
	// Mark the document parsing.
	if _, err := tx.Exec(ctx, `UPDATE document SET status='parsing', phase='parsing' WHERE id=$1`, docID); err != nil {
		return claimed{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return claimed{}, false, err
	}
	// The raw content for non-url sources is stored inline in document.content at
	// enqueue time; the worker re-reads it here to parse asynchronously. For url
	// sources content is empty (the URL is in sourceRef).
	c.raw = w.loadRaw(ctx, docID)
	return c, true, nil
}

// loadRaw retrieves the raw bytes to parse. Content is stored in the document
// row's content column (written at enqueue time). For url sources raw is empty
// (the URL is in sourceRef).
func (w *Worker) loadRaw(ctx context.Context, docID string) []byte {
	var raw []byte
	if err := w.cfg.Pool.QueryRow(ctx, `SELECT content FROM document WHERE id=$1`, docID).Scan(&raw); err != nil {
		w.cfg.Logger.Warn("ingest: load raw content failed", "doc", docID, "err", err)
	}
	return raw
}

// process parses + imports the claimed job and transitions state. On failure it
// reschedules with backoff or marks dead past MaxAttempts.
func (w *Worker) process(ctx context.Context, c claimed) {
	deps := parseDeps{fetcher: w.cfg.Fetcher, parseTimeout: w.cfg.ParseTimeout}
	text, _, perr := parseSource(ctx, deps, c.sourceType, c.raw, c.sourceRef)
	if perr == nil {
		w.setPhase(ctx, c.docID, c.jobID, "indexing")
		doc := makeDocument(c.docID, c.kbID, c.sourceType, c.title, text)
		var res ragingest.ImportResult
		res, perr = w.cfg.Rag.Import(ctx, []ragingest.Document{doc}, ragingest.ImportOptions{
			Namespace: c.namespace, ReplaceSource: true,
		})
		if perr == nil {
			// Atomic success path: the document→ready and ingest_job→done writes
			// must commit together. Two separate Execs left a crash window where
			// the job stayed 'running' after a successful Import and would be
			// re-imported on reclaim (harmless via ReplaceSource, but wasteful).
			// Persist checksum so the dedup short-circuit (Enqueue) can match;
			// null out content so the raw bytes are not retained after indexing
			// (content_bytes still drives the quota; content is only needed for
			// the async parse/retry window).
			tx, err := w.cfg.Pool.Begin(ctx)
			if err != nil {
				w.cfg.Logger.Error("ingest: begin success tx failed", "job", c.jobID, "err", err)
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx,
				`UPDATE document SET status='ready', phase='ready', chunk_count=$2, checksum=$3, error='', content=NULL WHERE id=$1`,
				c.docID, res.Chunks, checksum(text)); err != nil {
				w.cfg.Logger.Error("ingest: mark ready failed", "doc", c.docID, "err", err)
				return
			}
			if _, err := tx.Exec(ctx,
				`UPDATE ingest_job SET state='done', phase='ready', locked_by='', locked_until=NULL, updated_at=now() WHERE id=$1`,
				c.jobID); err != nil {
				w.cfg.Logger.Error("ingest: mark job done failed", "job", c.jobID, "err", err)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				w.cfg.Logger.Error("ingest: commit success tx failed", "job", c.jobID, "err", err)
				return
			}
			return
		}
	}
	w.fail(ctx, c, perr)
}

func (w *Worker) setPhase(ctx context.Context, docID, jobID, phase string) {
	if _, err := w.cfg.Pool.Exec(ctx, `UPDATE document SET phase=$2 WHERE id=$1`, docID, phase); err != nil {
		w.cfg.Logger.Error("ingest: set document phase failed", "doc", docID, "phase", phase, "err", err)
	}
	if _, err := w.cfg.Pool.Exec(ctx, `UPDATE ingest_job SET phase=$2, updated_at=now() WHERE id=$1`, jobID, phase); err != nil {
		w.cfg.Logger.Error("ingest: set job phase failed", "job", jobID, "phase", phase, "err", err)
	}
}

// fail either reschedules the job with exponential backoff (attempts < max) or
// marks it dead (and the document failed). Backoff uses the injected clock.
func (w *Worker) fail(ctx context.Context, c claimed, cause error) {
	msg := "unknown error"
	if cause != nil {
		msg = cause.Error()
	}
	if c.attempts >= w.cfg.MaxAttempts {
		if _, err := w.cfg.Pool.Exec(ctx,
			`UPDATE ingest_job SET state='dead', last_error=$2, locked_by='', locked_until=NULL, updated_at=now() WHERE id=$1`,
			c.jobID, msg); err != nil {
			w.cfg.Logger.Error("ingest: mark job dead failed", "job", c.jobID, "err", err)
		}
		if _, err := w.cfg.Pool.Exec(ctx,
			`UPDATE document SET status='failed', phase='failed', error=$2 WHERE id=$1`, c.docID, msg); err != nil {
			w.cfg.Logger.Error("ingest: mark document failed failed", "doc", c.docID, "err", err)
		}
		return
	}
	backoff := w.cfg.BaseBackoff * (1 << (c.attempts - 1)) // base * 2^(attempts-1)
	nextRun := w.cfg.Clock().Add(backoff)
	if _, err := w.cfg.Pool.Exec(ctx,
		`UPDATE ingest_job SET state='pending', next_run_at=$2, last_error=$3, locked_by='', locked_until=NULL, updated_at=now() WHERE id=$1`,
		c.jobID, nextRun, msg); err != nil {
		w.cfg.Logger.Error("ingest: reschedule job failed", "job", c.jobID, "err", err)
	}
	if _, err := w.cfg.Pool.Exec(ctx,
		`UPDATE document SET status='pending', phase='retry-scheduled', error=$2 WHERE id=$1`, c.docID, msg); err != nil {
		w.cfg.Logger.Error("ingest: mark document retry-scheduled failed", "doc", c.docID, "err", err)
	}
}
