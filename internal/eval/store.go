package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists eval_run rows (§5).
type Store struct{ pool *pgxpool.Pool }

// NewStore builds an eval_run repo.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// InsertInput is one eval_run row to persist. MetricsJSON is the kind-specific
// metric sub-struct (retrieval=MetricsView, triad/global/drift=GenerationView,
// drift additionally stores the BenchmarkResult under "benchmark"). DriftJSON is
// the serialized DriftView (only for kind=drift; nil otherwise).
type InsertInput struct {
	KBID        string
	Kind        Kind
	DatasetName string
	MetricsJSON []byte
	DriftJSON   []byte // nil for non-drift kinds
}

// RunRow is one persisted eval_run row, as read back for listing.
type RunRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DatasetName string `json:"datasetName"`
	MetricsJSON []byte `json:"-"`
	DriftJSON   []byte `json:"-"`
	CreatedAt   string `json:"createdAt"`
}

// Insert writes one eval_run row and returns its id.
func (s *Store) Insert(ctx context.Context, in InsertInput) (string, error) {
	id := newID()
	var driftArg any
	if in.DriftJSON != nil {
		driftArg = in.DriftJSON
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO eval_run (id, kb_id, kind, dataset_name, metrics_json, drift_json)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, in.KBID, string(in.Kind), in.DatasetName, in.MetricsJSON, driftArg)
	if err != nil {
		return "", fmt.Errorf("eval: insert run: %w", err)
	}
	return id, nil
}

// ListByKB returns up to limit runs for a kb, newest first, keyset-paginated by
// the (created_at, id) compound cursor encoded as id (id is a random hex so it
// is unique; we order by created_at DESC, id DESC and page on id). Empty cursor
// starts from the newest.
func (s *Store) ListByKB(ctx context.Context, kbID string, limit int, cursor string) ([]RunRow, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	// Page on id: ORDER BY created_at DESC, id DESC; cursor is the last id seen.
	// Using id as the keyset key keeps it simple and stable (id is unique).
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, dataset_name, metrics_json, drift_json, created_at::text
		 FROM eval_run
		 WHERE kb_id = $1 AND ($2 = '' OR id < $2)
		 ORDER BY id DESC
		 LIMIT $3`, kbID, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		var drift []byte
		if err := rows.Scan(&r.ID, &r.Kind, &r.DatasetName, &r.MetricsJSON, &drift, &r.CreatedAt); err != nil {
			return nil, "", err
		}
		r.DriftJSON = drift
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// LatestBenchmark returns the metrics_json of the most recent drift-kind run for
// kb+dataset (the stored BenchmarkResult under "benchmark"). ok=false when no
// prior drift run exists — the first drift run has no baseline to compare.
func (s *Store) LatestBenchmark(ctx context.Context, kbID, datasetName string) ([]byte, bool, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT metrics_json FROM eval_run
		 WHERE kb_id = $1 AND dataset_name = $2 AND kind = 'drift'
		 ORDER BY id DESC LIMIT 1`, kbID, datasetName).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}
