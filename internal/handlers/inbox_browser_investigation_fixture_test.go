package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// An explicit test-binary fixture, never compiled into the application image.
// It exercises the real preparation/worker with an injected in-memory provider;
// the separately running HTTP application keeps every dispatch switch held.
func TestBrowserInvestigationFixture(t *testing.T) {
	out := os.Getenv("INBOX_INVESTIGATION_FIXTURE_OUT")
	if out == "" {
		t.Skip("Explicit owned browser fixture only")
	}
	require.Equal(t, "synthetic-held", os.Getenv("INBOX_BROWSER_FIXTURE_MODE"))
	require.Equal(t, "investigation-fixture.json", filepath.Base(out))
	require.True(t, filepath.IsAbs(out))
	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, err)
	owned := strings.HasPrefix(dsn.Hostname(), "whatomate-inbox-") && dsn.Path == "/fixture"
	ci := (dsn.Hostname() == "127.0.0.1" || dsn.Hostname() == "localhost") && dsn.Path == "/whatomate_test"
	require.True(t, owned || ci, "Only the named owned test database or loopback CI database is permitted")
	key := os.Getenv("INBOX_TEST_ENCRYPTION_KEY")
	require.GreaterOrEqual(t, len(key), 32)
	db, err := gorm.Open(postgres.Open(dsn.String()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var actor models.User
	require.NoError(t, db.Where("email=? AND is_super_admin", "admin@admin.com").First(&actor).Error)
	var org models.Organization
	require.NoError(t, db.First(&org, "id=?", actor.OrganizationID).Error)
	if org.Settings == nil {
		org.Settings = models.JSONB{}
	}
	previousDispatch, hadDispatch := org.Settings["inbox_integrations_enabled"]
	org.Settings["inbox_integrations_enabled"] = true
	require.NoError(t, db.Model(&org).Updates(map[string]any{"settings": org.Settings, "inbox_workflows_enabled": true}).Error)
	settings := &config.Config{}
	settings.App.EncryptionKey = key
	settings.InboxAutomation.IntegrationDispatchEnabled = true
	settings.InboxAutomation.IntegrationEndpoints = `[{"key":"synthetic_crm","name":"Synthetic service","url":"https://service.example.invalid/requests","method":"POST"}]`
	a := &App{DB: db, Config: settings, Log: testutil.NopLogger()}
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	require.NoError(t, db.Model(account).Update("name", "Synthetic investigation account").Error)
	fields := []models.InboxFieldDefinition{
		{OrganizationID: org.ID, Scope: "case", Key: "verified_count", Label: "Verified count", Kind: "number"},
		{OrganizationID: org.ID, Scope: "contact", Key: "verified_flag", Label: "Verified flag", Kind: "boolean"},
		{OrganizationID: org.ID, Scope: "case", Key: "verified_text", Label: "Verified text", Kind: "text"},
		{OrganizationID: org.ID, Scope: "case", Key: "verified_date", Label: "Verified date", Kind: "date"},
		{OrganizationID: org.ID, Scope: "case", Key: "verified_option", Label: "Verified option", Kind: "select", Options: []string{"Pending", "Qualified"}},
	}
	for i := range fields {
		require.NoError(t, db.Create(&fields[i]).Error)
	}
	type scenario struct {
		RunID, ExecutionID, CaseID, ContactID uuid.UUID
		Name                                  string
	}
	scenarios := map[string]scenario{}
	providerCalls := 0
	for _, name := range []string{"typed", "no-effect", "provider-success"} {
		contact := testutil.CreateTestContact(t, db, org.ID)
		require.NoError(t, db.Model(contact).Update("profile_name", "Synthetic investigation "+name).Error)
		c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), &actor.ID, nil)
		require.NoError(t, db.Create(&c).Error)
		initial := []json.RawMessage{json.RawMessage(`7`), json.RawMessage(`true`), json.RawMessage(`"Existing value"`), json.RawMessage(`"2026-11-20"`), json.RawMessage(`"Pending"`)}
		for i, field := range fields {
			subject := c.ID
			if field.Scope == "contact" {
				subject = c.ContactID
			}
			require.NoError(t, db.Create(&models.InboxFieldValue{OrganizationID: org.ID, DefinitionID: field.ID, SubjectID: subject, Value: initial[i]}).Error)
		}
		d := models.InboxIntegration{OrganizationID: org.ID, CreatedByID: actor.ID, Name: "Synthetic investigation " + name, EndpointKey: "synthetic_crm", AuthMode: "none", Enabled: true, Inputs: []models.InboxIntegrationInput{}, Outputs: []models.InboxIntegrationOutput{}, ReferenceKey: "reference"}
		for _, field := range fields {
			d.Outputs = append(d.Outputs, models.InboxIntegrationOutput{Name: field.Key, DefinitionID: field.ID})
		}
		require.NoError(t, db.Create(&d).Error)
		w := models.InboxWorkflow{OrganizationID: org.ID, CreatedByID: actor.ID, Kind: "macro", Name: "Synthetic investigation " + name, Visibility: "organization", Actions: []models.InboxAction{{Type: "integration", Value: d.ID.String(), IntegrationInputs: map[string]string{}}, {Type: "note", Value: "After synthetic investigated operation"}}}
		require.NoError(t, db.Create(&w).Error)
		require.NoError(t, db.First(&w, "id=?", w.ID).Error)
		f := integrationFixture{a: a, c: c, actor: &actor, d: d, w: w, mappings: map[string]string{}}
		a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
			providerCalls++
			if name != "provider-success" {
				return inboxintegration.Response{}, errors.New("Synthetic provider response lost")
			}
			// Preserve a genuine fake-provider success but hold its outputs after
			// a concurrent case edit, so the normal HTTP review can discard them.
			require.NoError(t, db.Model(&c).Updates(map[string]any{"priority": "high", "version": gorm.Expr("version+1")}).Error)
			return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"FAKE-PROVIDER-1"`), "verified_count": json.RawMessage(`0`), "verified_flag": json.RawMessage(`false`), "verified_text": json.RawMessage(`"Provider value"`), "verified_date": json.RawMessage(`"2026-11-24"`), "verified_option": json.RawMessage(`"Qualified"`)}}, nil
		})
		run := queueSequenceMacro(t, f)
		paused := driveSequence(t, f, run.ID, 3)
		var execution models.InboxIntegrationExecution
		require.NoError(t, db.First(&execution, "run_id=?", run.ID).Error)
		require.Equal(t, 1, execution.AttemptCount)
		if name == "provider-success" {
			require.Equal(t, "succeeded", execution.State)
			require.Nil(t, execution.OutputsAppliedAt)
		} else {
			require.Equal(t, "unknown", execution.State)
		}
		require.Contains(t, []string{inboxSequenceUnknown, inboxSequenceReview, inboxSequenceBlocked}, paused.State)
		scenarios[name] = scenario{run.ID, execution.ID, c.ID, c.ContactID, w.Name}
	}
	require.Equal(t, 3, providerCalls)
	// Return organisation dispatch to its original held setting. Other browser
	// journeys share the owned database but must not inherit fixture dispatch.
	if hadDispatch {
		org.Settings["inbox_integrations_enabled"] = previousDispatch
	} else {
		delete(org.Settings, "inbox_integrations_enabled")
	}
	require.NoError(t, db.Model(&org).Update("settings", org.Settings).Error)
	metadata := map[string]any{"organization_id": org.ID, "account_id": account.ID, "scenarios": scenarios, "fields": fields, "fake_provider_calls": providerCalls, "synthetic": true}
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)
	file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = file.Write(raw)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}
