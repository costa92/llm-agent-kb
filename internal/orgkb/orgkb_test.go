package orgkb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	authzrole "github.com/costa92/llm-agent-authz/role"
	authzstore "github.com/costa92/llm-agent-authz/store"
)

const liveEnvVar = "LLM_AGENT_KB_PG_URL"

func openRepo(t *testing.T, ctx context.Context) (*Repo, *authzstore.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(liveEnvVar)
	if dsn == "" {
		t.Skipf("set %s to run live tests", liveEnvVar)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, tbl := range []string{"document", "knowledge_base", "auth_membership", "auth_session", "auth_user", "auth_org"} {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS authz_schema_version")
	az := authzstore.New(pool)
	if err := az.Migrate(ctx); err != nil {
		t.Fatalf("authz migrate: %v", err)
	}
	// kb business tables.
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS knowledge_base (
		id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL,
		namespace TEXT NOT NULL UNIQUE, embedding_model TEXT NOT NULL DEFAULT '',
		embedding_dim INT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create knowledge_base: %v", err)
	}
	return New(pool, az), az, pool
}

func TestCreateKBWritesAdminMembership(t *testing.T) {
	ctx := context.Background()
	repo, az, _ := openRepo(t, ctx)
	uid, err := az.CreateUser(ctx, "creator@x.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	oid, err := az.CreateOrg(ctx, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := repo.Create(ctx, CreateInput{OrgID: oid, Name: "Docs", CreatorUserID: uid, EmbeddingModel: "nomic", EmbeddingDim: 8})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if kb.Namespace == "" || kb.ID == "" {
		t.Fatalf("kb missing id/namespace: %+v", kb)
	}
	// Creator must be admin on the new kb scope.
	got, err := az.ResolveRole(ctx, uid, oid, "kb", kb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != authzrole.RoleAdmin {
		t.Fatalf("creator role=%q want admin", got)
	}
}

func TestOrgIDForKB(t *testing.T) {
	ctx := context.Background()
	repo, az, _ := openRepo(t, ctx)
	uid, _ := az.CreateUser(ctx, "c@x.com", "h")
	oid, _ := az.CreateOrg(ctx, "Acme")
	kb, _ := repo.Create(ctx, CreateInput{OrgID: oid, Name: "D", CreatorUserID: uid, EmbeddingDim: 8})
	gotOrg, err := repo.OrgIDForKB(ctx, kb.ID)
	if err != nil || gotOrg != oid {
		t.Fatalf("OrgIDForKB=%q,%v want %q", gotOrg, err, oid)
	}
	if _, err := repo.OrgIDForKB(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("missing kb err=%v want ErrNotFound", err)
	}
}

func TestListAndDelete(t *testing.T) {
	ctx := context.Background()
	repo, az, _ := openRepo(t, ctx)
	uid, _ := az.CreateUser(ctx, "c@x.com", "h")
	oid, _ := az.CreateOrg(ctx, "Acme")
	a, _ := repo.Create(ctx, CreateInput{OrgID: oid, Name: "A", CreatorUserID: uid, EmbeddingDim: 8})
	_, _ = repo.Create(ctx, CreateInput{OrgID: oid, Name: "B", CreatorUserID: uid, EmbeddingDim: 8})
	items, _, err := repo.ListByOrg(ctx, oid, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items)=%d want 2", len(items))
	}
	// Creator has kb-scope admin on A before delete.
	if got, _ := az.ResolveRole(ctx, uid, oid, "kb", a.ID); got != authzrole.RoleAdmin {
		t.Fatalf("pre-delete role=%q want admin", got)
	}
	if err := repo.DeleteRow(ctx, a.ID); err != nil {
		t.Fatalf("DeleteRow: %v", err)
	}
	if _, err := repo.Get(ctx, a.ID); err != ErrNotFound {
		t.Fatalf("deleted kb get err=%v want ErrNotFound", err)
	}
	// §16.4 (M1): the kb-scope membership is gone after delete.
	if got, _ := az.ResolveRole(ctx, uid, oid, "kb", a.ID); got != authzrole.RoleNone {
		t.Fatalf("post-delete role=%q want none (membership must be removed)", got)
	}
}

func TestCreateOrgGrantsOrgAdminWhoCanCreateKB(t *testing.T) {
	ctx := context.Background()
	repo, az, _ := openRepo(t, ctx)
	uid, _ := az.CreateUser(ctx, "boss@x.com", "h")
	oid, err := repo.CreateOrg(ctx, "Acme", uid)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	// Org creator is an org-level org_admin: org-level row (scope_id NULL)
	// matches any kb-scope resolve, and org_admin outranks admin.
	got, err := az.ResolveRole(ctx, uid, oid, "kb", "any-kb-id")
	if err != nil {
		t.Fatal(err)
	}
	if got != authzrole.RoleOrgAdmin {
		t.Fatalf("org creator role=%q want org_admin", got)
	}
	if !got.AtLeast(authzrole.RoleAdmin) {
		t.Fatal("org_admin must satisfy admin minimum for kb-create")
	}
	// And the org_admin can create a kb in the org.
	kb, err := repo.Create(ctx, CreateInput{OrgID: oid, Name: "Docs", CreatorUserID: uid, EmbeddingDim: 8})
	if err != nil || kb.ID == "" {
		t.Fatalf("org_admin Create kb: %v", err)
	}
}
