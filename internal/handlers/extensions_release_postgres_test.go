package handlers

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProtectedReleasePersistsBoundaryAndRefusesOrdinaryRestart(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("er_release_%d", time.Now().UnixNano())
	if _, err = base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	for _, table := range []string{"contacts", "messages"} {
		if err = db.Exec(`CREATE TABLE ` + table + ` (id uuid PRIMARY KEY,organization_id uuid NOT NULL,protection_organization_id text NOT NULL DEFAULT '',protection_module text NOT NULL DEFAULT '',protection_resource text NOT NULL DEFAULT '',protection_classification text NOT NULL DEFAULT '')`).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = models.InstallProtectionGuards(db); err != nil {
		t.Fatal(err)
	}
	c := config.ExtensionsConfig{ProtectedMode: true, SecurityKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), MFAFreshnessSeconds: 900, Modules: "directory,cases,knowledge,ai,conversation,reports,governance", RosterFreshnessSeconds: 86400, EscalationAfterSeconds: 3600, ConversationEmployeeAudience: "employees", ReportsMinGroup: 5, AIProvider: "https", AIEndpoint: "https://fixture.invalid/v1/chat/completions", AIEmbeddingEndpoint: "https://fixture.invalid/v1/embeddings", AIAllowedEndpoints: []string{"https://fixture.invalid/v1/chat/completions", "https://fixture.invalid/v1/embeddings"}, AIDeploymentAttestation: "TEST-FIXTURE-NOT-DEPLOYMENT-EVIDENCE", AICountry: "ZA", AIModel: "fixture-chat", AIEmbeddingModel: "fixture-embedding", AIEmbeddingDimensions: 3, AIPromptRevision: "fixture-v1"}
	a := &App{DB: db, Config: &config.Config{Extensions: c}}
	// An enabled vector backend must pass startup verification before the
	// irreversible protected marker is activated.
	invalidVectors := c
	invalidVectors.KnowledgeVectorEnabled = true
	if err = (&App{DB: db, Config: &config.Config{Extensions: invalidVectors}}).InitializeExtensions(); err == nil {
		t.Fatal("invalid vector startup activated protected release")
	}
	if protected, e := models.ProtectedHostEnabled(db); e != nil || protected {
		t.Fatalf("failed composition changed marker: %v", e)
	}
	if err = a.InitializeExtensions(); err != nil {
		t.Fatal(err)
	}
	if a.ExtensionConversation.Config.EnabledSyntheticDevelopment {
		t.Fatal("production enabled simulation endpoints")
	}
	if models.GenericOrganizationAllowed(context.Background(), db, uuid.New()) {
		t.Fatal("empty protected host admitted generic access")
	}
	if err = (&App{DB: db, Config: &config.Config{}}).InitializeExtensions(); err == nil {
		t.Fatal("ordinary restart ignored persistent protection")
	}
	if err = (&App{DB: db, Config: &config.Config{Extensions: c}}).InitializeExtensions(); err != nil {
		t.Fatalf("same protected composition failed restart: %v", err)
	}
	global := c
	global.AIProcessingScope = "global_standard"
	global.AIInternationalProcessingApproval = "synthetic-reviewed-demo-approval"
	global.AIInternationalProcessingDisclosure = "Synthetic deployment: generation and embeddings may process internationally."
	globalApp := &App{DB: db, Config: &config.Config{Extensions: global}}
	if err = globalApp.InitializeExtensions(); err != nil {
		t.Fatal(err)
	}
	notice := globalApp.ExtensionConversation.Config.ProcessingDisclosure
	if notice == nil || notice.Revision != "global-standard-v1" || notice.Text != extensionGlobalAIProcessingNotice {
		t.Fatal("Global Standard conversation omitted canonical processing disclosure")
	}
	if a.ExtensionConversation.Config.ProcessingDisclosure != nil {
		t.Fatal("ZA-only composition inherited global processing notice")
	}
}
