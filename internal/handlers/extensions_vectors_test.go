package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func hostVectorFixture(t *testing.T) (*App, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("VECTOR_DATABASE_URL")
	if dsn == "" {
		t.Skip("VECTOR_DATABASE_URL actual pgvector server required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("host_vector_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	query := u.Query()
	query.Set("search_path", schema+",public")
	u.RawQuery = query.Encode()
	gdb, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	if _, err = db.Exec(`CREATE TABLE organizations(id uuid PRIMARY KEY,deleted_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	org := uuid.NewString()
	if _, err = db.Exec(`INSERT INTO organizations(id) VALUES($1)`, org); err != nil {
		t.Fatal(err)
	}
	if err = knowledge.MigrateHTTP(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	c := validProductionAIHostConfig()
	c.Modules = "knowledge,ai"
	c.KnowledgeVectorEnabled = true
	c.KnowledgeVectorIdentity = "synthetic-reviewed-embedding-deployment@revision-1"
	c.AIEmbeddingDimensions = 2
	a := &App{DB: gdb, Config: &config.Config{Extensions: c}}
	return a, db, org
}

func TestExtensionVectorsDisabledDefaultAndInvalidPins(t *testing.T) {
	ctx := context.Background()
	a := &App{Config: &config.Config{}}
	if err := a.initializeExtensionVectors(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if repo, err := a.extensionVectors(ctx, nil, "arbitrary"); err != nil || repo != nil {
		t.Fatal("disabled backend touched database")
	}
	if err := a.extensionVectorTick(ctx); err != nil {
		t.Fatal("disabled tick touched database")
	}
	a.Config.Extensions.KnowledgeVectorEnabled = true
	if err := a.initializeExtensionVectors(ctx, nil); err == nil {
		t.Fatal("unconfigured vector backend enabled")
	}
}

func TestExtensionVectorsActualPGStartupNoRequestDDLAndRevokedWorker(t *testing.T) {
	a, db, org := hostVectorFixture(t)
	ctx := context.Background()
	if _, err := a.extensionVectors(ctx, db, org); err == nil {
		t.Fatal("request initialized missing profile")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='addon_knowledge_vector_profiles'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("request performed vector DDL")
	}
	if err := a.initializeExtensionVectors(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := a.extensionVectors(ctx, db, org)
	if err != nil || repo == nil {
		t.Fatalf("initialized vector helper denied: %v", err)
	}
	if err = repo.DisableIndexer(ctx, extensionVectorIndexer); err != nil {
		t.Fatal(err)
	}
	if err = a.initializeExtensionVectors(ctx, db); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err = db.QueryRow(`SELECT enabled FROM addon_knowledge_vector_workers WHERE tenant=$1 AND principal=$2`, org, extensionVectorIndexer).Scan(&enabled); err != nil || enabled {
		t.Fatal("startup reenabled revoked trusted worker")
	}
	if err = a.extensionVectorTick(ctx); err == nil {
		t.Fatal("revoked worker allowed indexing tick")
	}
}

func TestExtensionVectorsUnknownDeletedOrgCrossPoolAndPinChangeDeny(t *testing.T) {
	a, db, org := hostVectorFixture(t)
	ctx := context.Background()
	if err := a.initializeExtensionVectors(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"", uuid.NewString(), "not-uuid"} {
		if _, err := a.extensionVectors(ctx, db, tenant); err == nil {
			t.Fatal("untrusted organization accepted")
		}
	}
	other, otherDB, _ := hostVectorFixture(t)
	if _, err := a.extensionVectors(ctx, otherDB, org); err == nil {
		t.Fatal("different host pool accepted")
	}
	if _, err := other.extensionVectors(ctx, otherDB, org); err == nil {
		t.Fatal("organization from parent host accepted")
	}
	a.Config.Extensions.KnowledgeVectorIdentity = "changed-model-generation"
	if _, err := a.extensionVectors(ctx, db, org); err == nil {
		t.Fatal("pinned generation changed without reindex")
	}
	a.Config.Extensions.KnowledgeVectorIdentity = "synthetic-reviewed-embedding-deployment@revision-1"
	a.Config.Extensions.AIEmbeddingDimensions = 3
	if _, err := a.extensionVectors(ctx, db, org); err == nil {
		t.Fatal("pinned dimensions changed without reindex")
	}
	a.Config.Extensions.AIEmbeddingDimensions = 2
	a.Config.Extensions.KnowledgeVectorIdentity = a.Config.Extensions.AIEmbeddingModel
	if err := a.initializeExtensionVectors(ctx, db); err == nil {
		t.Fatal("logical model alias accepted as identity proof")
	}
	a.Config.Extensions.KnowledgeVectorIdentity = "synthetic-reviewed-embedding-deployment@revision-1"
	if _, err := db.Exec(`UPDATE organizations SET deleted_at=clock_timestamp() WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := a.extensionVectors(ctx, db, org); err == nil {
		t.Fatal("deleted organization accepted")
	}
}

func TestExtensionVectorsNewTenantHasNoRequestMigrationAndTickFailuresAreIndependent(t *testing.T) {
	a, db, first := hostVectorFixture(t)
	ctx := context.Background()
	second := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO organizations(id) VALUES($1)`, second); err != nil {
		t.Fatal(err)
	}
	if err := a.initializeExtensionVectors(ctx, db); err != nil {
		t.Fatal(err)
	}
	one, err := a.extensionVectors(ctx, db, first)
	if err != nil {
		t.Fatal(err)
	}
	if err = one.DisableIndexer(ctx, extensionVectorIndexer); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM addon_knowledge_vector_workers WHERE tenant=$1;`, second); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM addon_knowledge_vector_profiles WHERE tenant=$1`, second); err != nil {
		t.Fatal(err)
	}
	if _, err = a.extensionVectors(ctx, db, second); !errors.Is(err, knowledge.ErrVectorProfile) {
		t.Fatal("request initialized new tenant profile")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM addon_knowledge_vector_profiles WHERE tenant=$1`, second).Scan(&count); err != nil || count != 0 {
		t.Fatal("request recreated profile")
	}
	err = a.extensionVectorTick(ctx)
	if !errors.Is(err, knowledge.ErrDenied) || !errors.Is(err, knowledge.ErrVectorProfile) {
		t.Fatal("one tenant failure starved remaining tenant verification")
	}
}
