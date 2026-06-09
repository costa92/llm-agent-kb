// Package orgkb is the knowledge-base resource domain: create/list/get/delete
// kb rows and write the creator's admin membership via authz. It never touches
// vectors (spec §4).
package orgkb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authzrole "github.com/costa92/llm-agent-authz/role"
	authzstore "github.com/costa92/llm-agent-authz/store"
)

// ErrNotFound is returned when a kb row does not exist.
var ErrNotFound = errors.New("orgkb: not found")

// KB is a knowledge_base row.
type KB struct {
	ID             string
	OrgID          string
	Name           string
	Namespace      string
	EmbeddingModel string
	EmbeddingDim   int
}

// CreateInput is the input to Create.
type CreateInput struct {
	OrgID          string
	Name           string
	CreatorUserID  string
	EmbeddingModel string
	EmbeddingDim   int
}

// Repo persists knowledge bases and writes authz memberships.
type Repo struct {
	pool  *pgxpool.Pool
	authz *authzstore.Store
}

// New builds a Repo.
func New(pool *pgxpool.Pool, az *authzstore.Store) *Repo { return &Repo{pool: pool, authz: az} }

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Create inserts a kb row and grants the creator admin on the new kb scope,
// in one transaction. namespace = "kb_" + id so it is unique and stable.
func (r *Repo) Create(ctx context.Context, in CreateInput) (KB, error) {
	if in.OrgID == "" || in.Name == "" || in.CreatorUserID == "" {
		return KB{}, fmt.Errorf("orgkb: OrgID, Name, CreatorUserID required")
	}
	kb := KB{
		ID:             newID(),
		OrgID:          in.OrgID,
		Name:           in.Name,
		EmbeddingModel: in.EmbeddingModel,
		EmbeddingDim:   in.EmbeddingDim,
	}
	kb.Namespace = "kb_" + kb.ID

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return KB{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`INSERT INTO knowledge_base (id, org_id, name, namespace, embedding_model, embedding_dim)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		kb.ID, kb.OrgID, kb.Name, kb.Namespace, kb.EmbeddingModel, kb.EmbeddingDim); err != nil {
		return KB{}, fmt.Errorf("orgkb: insert kb: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KB{}, err
	}
	// Membership is in the authz schema; UpsertMembership is idempotent so a
	// retry is safe. scope_id is the kb id.
	kbID := kb.ID
	if err := r.authz.UpsertMembership(ctx, kb.OrgID, in.CreatorUserID, "kb", &kbID, authzrole.RoleAdmin); err != nil {
		return KB{}, fmt.Errorf("orgkb: grant creator admin: %w", err)
	}
	return kb, nil
}

// Get returns a kb row by id.
func (r *Repo) Get(ctx context.Context, id string) (KB, error) {
	var kb KB
	err := r.pool.QueryRow(ctx,
		`SELECT id, org_id, name, namespace, embedding_model, embedding_dim
		 FROM knowledge_base WHERE id = $1`, id).
		Scan(&kb.ID, &kb.OrgID, &kb.Name, &kb.Namespace, &kb.EmbeddingModel, &kb.EmbeddingDim)
	if errors.Is(err, pgx.ErrNoRows) {
		return KB{}, ErrNotFound
	}
	return kb, err
}

// OrgIDForKB resolves the org_id for a kb (used by the RBAC middleware, which
// only has the kb id from the path).
func (r *Repo) OrgIDForKB(ctx context.Context, kbID string) (string, error) {
	var orgID string
	err := r.pool.QueryRow(ctx, `SELECT org_id FROM knowledge_base WHERE id = $1`, kbID).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return orgID, err
}

// ListByOrg returns up to limit kbs for an org, ordered by id, with a cursor
// (the last id seen). Empty cursor starts from the beginning.
func (r *Repo) ListByOrg(ctx context.Context, orgID string, limit int, cursor string) ([]KB, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, name, namespace, embedding_model, embedding_dim
		 FROM knowledge_base
		 WHERE org_id = $1 AND id > $2
		 ORDER BY id ASC
		 LIMIT $3`, orgID, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []KB
	for rows.Next() {
		var kb KB
		if err := rows.Scan(&kb.ID, &kb.OrgID, &kb.Name, &kb.Namespace, &kb.EmbeddingModel, &kb.EmbeddingDim); err != nil {
			return nil, "", err
		}
		out = append(out, kb)
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

// DeleteRow deletes the knowledge_base row AND the authz kb-scope memberships
// for that kb, in one transaction (§16.4: "删 knowledge_base 行 + authz 中该
// scope 的 membership"). The chunk/graph cascade runs BEFORE this (in the
// httpapi delete-kb handler via DeleteAllDocumentsForKB); this is the final
// step. auth_membership lives in the authz schema but shares the same pgxpool,
// so the delete is part of the same transaction.
func (r *Repo) DeleteRow(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM knowledge_base WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// Remove every kb-scope membership for this kb (creator admin + any granted).
	if _, err := tx.Exec(ctx,
		`DELETE FROM auth_membership WHERE scope_kind = 'kb' AND scope_id = $1`, id); err != nil {
		return fmt.Errorf("orgkb: delete kb memberships: %w", err)
	}
	return tx.Commit(ctx)
}

// CreateOrg is the org-bootstrap path (POST /api/orgs): it creates an org via
// the authz store and immediately writes the caller as an ORG-LEVEL admin
// (scope_kind="kb", scope_id=nil, role=org_admin). Org-level rows match every
// kb-scope ResolveRole (memberships query: `scope_id IS NULL OR scope_id=$4`),
// so the org creator becomes an org_admin who can create/list kbs in the org.
// This is the only seam by which the first org_admin can exist.
func (r *Repo) CreateOrg(ctx context.Context, name, creatorUserID string) (string, error) {
	if name == "" || creatorUserID == "" {
		return "", fmt.Errorf("orgkb: org name and creatorUserID required")
	}
	orgID, err := r.authz.CreateOrg(ctx, name)
	if err != nil {
		return "", fmt.Errorf("orgkb: create org: %w", err)
	}
	// scope_id=nil → org-level membership (matches any kb scope on resolve).
	if err := r.authz.UpsertMembership(ctx, orgID, creatorUserID, "kb", nil, authzrole.RoleOrgAdmin); err != nil {
		return "", fmt.Errorf("orgkb: grant creator org_admin: %w", err)
	}
	return orgID, nil
}
