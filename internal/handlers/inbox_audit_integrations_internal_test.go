package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func newAuditIntegrationFixture(t *testing.T) inboxAuditFixture {
	t.Helper()
	f := newInboxAuditAuthFixture(t)
	f.app.Config.App.EncryptionKey = strings.Repeat("k", 32)
	f.app.Config.InboxAutomation.IntegrationEndpoints = `[{"key":"crm","name":"Synthetic CRM","url":"https://crm.example.invalid/cases","method":"POST"}]`
	f.app.Config.Channels.StopDispatch = true
	return f
}

type auditIntegrationCase struct {
	resource, table        string
	create, update, remove func(*App, *fastglue.Request) error
	initial, updated       map[string]any
}

func auditIntegrationCases() []auditIntegrationCase {
	return []auditIntegrationCase{
		{"account", "whatsapp_accounts", (*App).CreateAccount, (*App).UpdateAccount, (*App).DeleteAccount,
			map[string]any{"name": "Synthetic account " + uuid.NewString(), "phone_id": uuid.NewString(), "business_id": uuid.NewString(), "access_token": "synthetic-private-account", "app_secret": "synthetic-private-meta"},
			map[string]any{"name": "Edited account " + uuid.NewString(), "access_token": "synthetic-private-rotated"}},
		{"webhook", "webhooks", (*App).CreateWebhook, (*App).UpdateWebhook, (*App).DeleteWebhook,
			map[string]any{"name": "Synthetic webhook", "url": "https://hooks.example.invalid/events", "events": []string{"message.received"}, "secret": "synthetic-private-webhook"},
			map[string]any{"name": "Edited webhook", "is_active": true, "secret": "synthetic-private-rotated"}},
		{"custom_action", "custom_actions", (*App).CreateCustomAction, (*App).UpdateCustomAction, (*App).DeleteCustomAction,
			map[string]any{"name": "Synthetic action", "action_type": "webhook", "is_active": true, "config": map[string]any{"url": "https://hooks.example.invalid/action", "headers": map[string]string{"Authorization": "synthetic-private-action"}}},
			map[string]any{"name": "Edited action", "is_active": true}},
		{"sso_provider", "sso_providers", (*App).UpdateSSOProvider, (*App).UpdateSSOProvider, (*App).DeleteSSOProvider,
			map[string]any{"client_id": "synthetic-client", "client_secret": "synthetic-private-sso", "is_enabled": true},
			map[string]any{"client_id": "edited-client", "client_secret": "synthetic-private-rotated", "is_enabled": true}},
		{"inbox_integration", "inbox_integrations", (*App).CreateInboxIntegration, (*App).UpdateInboxIntegration, (*App).DeleteInboxIntegration,
			map[string]any{"name": "Synthetic integration", "endpoint_key": "crm", "auth_mode": "bearer", "credential": "synthetic-private-integration"},
			map[string]any{"name": "Edited integration", "endpoint_key": "crm", "auth_mode": "bearer", "credential": "synthetic-private-rotated"}},
	}
}

func integrationAuditRequest(t *testing.T, f inboxAuditFixture, id uuid.UUID, body any) *fastglue.Request {
	t.Helper()
	r := auditUserRequest(t, f, id, body)
	testutil.SetPathParam(r, "integration_id", id.String())
	testutil.SetPathParam(r, "provider", "microsoft")
	return r
}

func integrationRowJSON(t *testing.T, f inboxAuditFixture, table string, id uuid.UUID) string {
	t.Helper()
	var raw string
	// table comes only from the fixed test matrix, never a request.
	require.NoError(t, f.db.Raw("SELECT row_to_json(t)::text FROM "+table+" t WHERE id=? AND organization_id=?", id, f.org.ID).Scan(&raw).Error)
	return raw
}

func failIntegrationCapture(t *testing.T, f inboxAuditFixture, kind audit.EventKind) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	name := "audit_integration_fault_" + uuid.NewString()
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.OrganizationID == f.org.ID && row.Event == string(kind) {
			calls.Add(1)
			_ = db.AddError(errors.New("synthetic capture unavailable"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.db.Callback().Create().Remove(name)) })
	return calls
}

func TestInboxAuditIntegrationFamiliesCaptureLifecycleWithoutSecrets(t *testing.T) {
	for _, spec := range auditIntegrationCases() {
		t.Run(spec.resource, func(t *testing.T) {
			f := newAuditIntegrationFixture(t)
			r := integrationAuditRequest(t, f, uuid.Nil, spec.initial)
			require.NoError(t, spec.create(f.app, r))
			require.Equal(t, 200, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
			rows := f.securityRows(t)
			require.Len(t, rows, 1)
			id := rows[0].ResourceID
			require.Equal(t, string(audit.Created), rows[0].Reason)
			update := integrationAuditRequest(t, f, id, spec.updated)
			require.NoError(t, spec.update(f.app, update))
			require.Equal(t, 200, update.RequestCtx.Response.StatusCode(), string(update.RequestCtx.Response.Body()))
			remove := integrationAuditRequest(t, f, id, nil)
			require.NoError(t, spec.remove(f.app, remove))
			require.Equal(t, 200, remove.RequestCtx.Response.StatusCode(), string(remove.RequestCtx.Response.Body()))
			rows = f.securityRows(t)
			require.Len(t, rows, 3)
			for _, row := range rows {
				require.Equal(t, spec.resource, row.ResourceType)
				require.Equal(t, id, row.ResourceID)
				require.True(t, row.ActorVerified)
				require.Equal(t, f.user.ID, row.UserID)
				require.Empty(t, row.Changes)
				if spec.resource == "account" {
					require.NotNil(t, row.AccountID)
					require.Equal(t, id, *row.AccountID)
				} else {
					require.Nil(t, row.AccountID)
				}
			}
			require.Equal(t, string(audit.Deleted), rows[2].Reason)
			encoded, err := json.Marshal(rows)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "synthetic-private-")
			var remaining int64
			require.NoError(t, f.db.Table(spec.table).Where("id=? AND organization_id=? AND deleted_at IS NULL", id, f.org.ID).Count(&remaining).Error)
			require.Zero(t, remaining)
		})
	}
}

func TestInboxAuditIntegrationCaptureFailureRollsBackCreateUpdateAndDelete(t *testing.T) {
	for _, spec := range auditIntegrationCases() {
		t.Run(spec.resource, func(t *testing.T) {
			f := newAuditIntegrationFixture(t)
			create := integrationAuditRequest(t, f, uuid.Nil, spec.initial)
			require.NoError(t, spec.create(f.app, create))
			require.Equal(t, 200, create.RequestCtx.Response.StatusCode())
			rows := f.securityRows(t)
			require.Len(t, rows, 1)
			id := rows[0].ResourceID
			before := integrationRowJSON(t, f, spec.table, id)
			calls := failIntegrationCapture(t, f, audit.IntegrationChanged)
			update := integrationAuditRequest(t, f, id, spec.updated)
			require.NoError(t, spec.update(f.app, update))
			require.GreaterOrEqual(t, update.RequestCtx.Response.StatusCode(), 500)
			require.EqualValues(t, 1, calls.Load(), "update must reach the deliberately failed capture")
			require.Equal(t, before, integrationRowJSON(t, f, spec.table, id))
			remove := integrationAuditRequest(t, f, id, nil)
			require.NoError(t, spec.remove(f.app, remove))
			require.GreaterOrEqual(t, remove.RequestCtx.Response.StatusCode(), 500)
			require.EqualValues(t, 2, calls.Load())
			require.Equal(t, before, integrationRowJSON(t, f, spec.table, id))
			require.Len(t, f.securityRows(t), 1)
			// A failed creation must not leave a profile with no corresponding event.
			fresh := newAuditIntegrationFixture(t)
			freshCalls := failIntegrationCapture(t, fresh, audit.IntegrationChanged)
			if spec.resource == "account" {
				spec.initial["phone_id"] = uuid.NewString()
				spec.initial["name"] = "Synthetic account " + uuid.NewString()
			}
			r := integrationAuditRequest(t, fresh, uuid.Nil, spec.initial)
			require.NoError(t, spec.create(fresh.app, r))
			require.GreaterOrEqual(t, r.RequestCtx.Response.StatusCode(), 500)
			require.EqualValues(t, 1, freshCalls.Load())
			var count int64
			require.NoError(t, fresh.db.Table(spec.table).Where("organization_id=?", fresh.org.ID).Count(&count).Error)
			require.Zero(t, count)
			require.Empty(t, fresh.securityRows(t))
		})
	}
}

func TestInboxAuditIntegrationSettingsPreserveUnrelatedSettingsAndRollback(t *testing.T) {
	f := newAuditIntegrationFixture(t)
	require.NoError(t, f.db.Model(f.org).Update("settings", models.JSONB{"untouched": "keep", "inbox_integrations_enabled": false}).Error)
	r := integrationAuditRequest(t, f, uuid.Nil, map[string]bool{"enabled": true})
	require.NoError(t, f.app.UpdateInboxIntegrationSettings(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	rows := f.securityRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, "inbox_integration_settings", rows[0].ResourceType)
	require.Equal(t, f.org.ID, rows[0].ResourceID)
	failOwnedSecurityKind(t, f, audit.IntegrationChanged)
	r = integrationAuditRequest(t, f, uuid.Nil, map[string]bool{"enabled": false})
	require.NoError(t, f.app.UpdateInboxIntegrationSettings(r))
	require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
	var current models.Organization
	require.NoError(t, f.db.First(&current, "id=?", f.org.ID).Error)
	require.Equal(t, true, current.Settings["inbox_integrations_enabled"])
	require.Equal(t, "keep", current.Settings["untouched"])
	require.False(t, current.InboxWorkflowsEnabled)
	require.False(t, f.app.Config.InboxAutomation.DispatchEnabled)
	require.False(t, f.app.Config.InboxAutomation.IntegrationDispatchEnabled)
}

type auditChannelProvider struct {
	channels.Provider
	calls, sends atomic.Int32
	validate     func(context.Context, channels.Connection) error
}

func (*auditChannelProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "audit_fake", Name: "Synthetic audit provider", Version: "1", APIVersion: 1, Channel: "sms"}
}
func (p *auditChannelProvider) Validate(ctx context.Context, c channels.Connection) (channels.ConnectionInfo, error) {
	p.calls.Add(1)
	if p.validate != nil {
		if err := p.validate(ctx, c); err != nil {
			return channels.ConnectionInfo{}, err
		}
	}
	return channels.ConnectionInfo{Capabilities: channels.Capabilities{Text: true, DeliveryReports: true, Reconciliation: true}}, nil
}
func (p *auditChannelProvider) Send(context.Context, channels.Connection, channels.Message) (channels.Submission, error) {
	p.sends.Add(1)
	return channels.Submission{}, errors.New("sending is forbidden in audit acceptance")
}

func TestInboxAuditChannelCreationPhasesAreTruthfulAtomicAndOutsideNetworkLocks(t *testing.T) {
	for _, mode := range []string{"success", "provider_failure", "create_capture_failure", "validation_capture_failure", "concurrent_change", "concurrent_delete"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuditIntegrationFixture(t)
			p := &auditChannelProvider{}
			registry := channels.NewRegistry()
			require.NoError(t, registry.Register(p))
			f.app.Channels = &channelapp.Service{DB: f.db, Providers: registry, EncryptionKey: f.app.Config.App.EncryptionKey, StopDispatch: true}
			if mode == "create_capture_failure" {
				failOwnedSecurityKind(t, f, audit.IntegrationChanged)
			}
			if mode == "validation_capture_failure" {
				failOwnedSecurityKind(t, f, audit.IntegrationValidated)
			}
			p.validate = func(ctx context.Context, c channels.Connection) error {
				id, err := uuid.Parse(c.ID)
				require.NoError(t, err)
				// An independent transaction can claim the row NOWAIT while the
				// fake provider is called; no configuration lock spans network I/O.
				require.NoError(t, f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var row models.ChannelConnection
					return tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&row, "id=?", id).Error
				}))
				rows := f.securityRows(t)
				require.Len(t, rows, 1)
				require.Equal(t, string(audit.Created), rows[0].Reason)
				if mode == "provider_failure" {
					return errors.New("synthetic-private-provider-error")
				}
				if mode == "concurrent_change" {
					require.NoError(t, f.db.Model(&models.ChannelConnection{}).Where("id=?", id).Update("name", "Operator changed configuration").Error)
				}
				if mode == "concurrent_delete" {
					require.NoError(t, f.db.Delete(&models.ChannelConnection{}, "id=?", id).Error)
				}
				return nil
			}
			r := integrationAuditRequest(t, f, uuid.Nil, map[string]any{"name": "Synthetic channel", "provider": "audit_fake", "max_segments": 3, "credentials": map[string]string{"client_secret": "synthetic-private-channel"}})
			require.NoError(t, f.app.CreateChannelConnection(r))
			rows := f.securityRows(t)
			if mode == "create_capture_failure" {
				require.Equal(t, 503, r.RequestCtx.Response.StatusCode())
				require.Zero(t, p.calls.Load())
				require.Empty(t, rows)
				var count int64
				require.NoError(t, f.db.Model(&models.ChannelConnection{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
				require.Zero(t, count)
			} else {
				require.EqualValues(t, 1, p.calls.Load())
				if mode == "success" {
					require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
					require.Len(t, rows, 2)
					require.Equal(t, string(audit.IntegrationValidated), rows[1].Event)
				} else {
					require.Len(t, rows, 1)
				}
				if mode == "validation_capture_failure" {
					require.Equal(t, 503, r.RequestCtx.Response.StatusCode())
				}
				if mode == "concurrent_delete" {
					require.Equal(t, 409, r.RequestCtx.Response.StatusCode())
					require.NotContains(t, string(r.RequestCtx.Response.Body()), "retained")
				}
				if mode == "provider_failure" || mode == "concurrent_change" {
					require.Equal(t, 422, r.RequestCtx.Response.StatusCode())
				}
				if mode != "concurrent_delete" {
					var stored models.ChannelConnection
					require.NoError(t, f.db.First(&stored, "id=?", rows[0].ResourceID).Error)
					require.Equal(t, mode == "success", stored.Enabled)
					require.True(t, crypto.IsEncrypted(stored.Credentials))
					if mode == "concurrent_change" {
						require.Equal(t, "Operator changed configuration", stored.Name)
					}
				}
			}
			require.Zero(t, p.sends.Load())
			require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
			raw, err := json.Marshal(rows)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "synthetic-private-")
			require.True(t, f.app.Channels.StopDispatch)
			require.NoError(t, f.app.Channels.Dispatch(context.Background(), time.Now().Add(time.Hour)))
			require.LessOrEqual(t, p.calls.Load(), int32(1))
			require.Zero(t, p.sends.Load())
		})
	}
}

func TestInboxAuditIntegrationTargetsAndExpectedVersionsCannotBeBorrowed(t *testing.T) {
	for _, spec := range auditIntegrationCases() {
		t.Run(spec.resource, func(t *testing.T) {
			f := newAuditIntegrationFixture(t)
			create := integrationAuditRequest(t, f, uuid.Nil, spec.initial)
			require.NoError(t, spec.create(f.app, create))
			require.Equal(t, 200, create.RequestCtx.Response.StatusCode())
			rows := f.securityRows(t)
			require.Len(t, rows, 1)
			id := rows[0].ResourceID
			before := integrationRowJSON(t, f, spec.table, id)
			var calls int
			write := func(*gorm.DB) error { calls++; return nil }
			foreign := testutil.CreateTestOrganization(t, f.db)
			foreignActor := testutil.CreateTestUser(t, f.db, foreign.ID, testutil.WithSuperAdmin())
			r := integrationAuditRequest(t, f, id, nil)
			require.ErrorIs(t, f.app.mutateAuditedIntegration(r, foreign.ID, foreignActor.ID, spec.resource, &id, audit.ConfigurationChanged, write), gorm.ErrRecordNotFound)
			require.ErrorIs(t, f.app.mutateAuditedIntegration(r, f.org.ID, f.user.ID, spec.resource, &id, audit.ConfigurationChanged, write, time.Unix(1, 0)), errAuditedIntegrationChanged)
			require.Zero(t, calls)
			require.Equal(t, before, integrationRowJSON(t, f, spec.table, id))
			require.Len(t, f.securityRows(t), 1)
			// Exercise the public write boundary as a freshly loaded agent.
			role := testutil.CreateAgentRole(t, f.db, f.org.ID)
			agent := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
			denied := integrationAuditRequest(t, f, id, spec.updated)
			testutil.SetFullAuthContext(denied, f.org.ID, agent.ID, agent.RoleID, false)
			require.NoError(t, spec.update(f.app, denied))
			require.Equal(t, 403, denied.RequestCtx.Response.StatusCode())
			require.Equal(t, before, integrationRowJSON(t, f, spec.table, id))
			for _, row := range f.securityRows(t) {
				require.NotEqual(t, string(audit.ConfigurationChanged), row.Reason)
			}
		})
	}
	f := newAuditIntegrationFixture(t)
	foreign := uuid.New()
	called := false
	err := f.app.mutateAuditedIntegration(integrationAuditRequest(t, f, foreign, nil), f.org.ID, f.user.ID, "inbox_integration_settings", &foreign, audit.ConfigurationChanged, func(*gorm.DB) error { called = true; return nil })
	require.ErrorIs(t, err, audit.ErrInvalidEvent)
	require.False(t, called)
}

func TestInboxAuditAccountDefaultChangesRollbackWithCapture(t *testing.T) {
	f := newAuditIntegrationFixture(t)
	first := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	second := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	require.NoError(t, f.db.Model(first).Updates(map[string]any{"is_default_incoming": true, "is_default_outgoing": true}).Error)
	beforeFirst := integrationRowJSON(t, f, "whatsapp_accounts", first.ID)
	beforeSecond := integrationRowJSON(t, f, "whatsapp_accounts", second.ID)
	calls := failIntegrationCapture(t, f, audit.IntegrationChanged)
	r := integrationAuditRequest(t, f, second.ID, map[string]any{"is_default_incoming": true, "is_default_outgoing": true})
	require.NoError(t, f.app.UpdateAccount(r))
	require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, beforeFirst, integrationRowJSON(t, f, "whatsapp_accounts", first.ID))
	require.Equal(t, beforeSecond, integrationRowJSON(t, f, "whatsapp_accounts", second.ID))
	require.Empty(t, f.securityRows(t))
}

func TestInboxAuditChannelDisableAndCancellationShareCaptureTransaction(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "capture_failure"}[fail], func(t *testing.T) {
			f := newAuditIntegrationFixture(t)
			c := models.ChannelConnection{OrganizationID: f.org.ID, Name: "Synthetic channel", Provider: "audit_fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
			require.NoError(t, f.db.Create(&c).Error)
			contact := testutil.CreateTestContact(t, f.db, f.org.ID)
			attempts := []models.ChannelAttempt{}
			for _, status := range []string{"queued", "unknown"} {
				m := models.Message{OrganizationID: f.org.ID, ContactID: contact.ID, Channel: "sms", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Synthetic held", Status: models.MessageStatusPending}
				require.NoError(t, f.db.Create(&m).Error)
				a := models.ChannelAttempt{OrganizationID: f.org.ID, MessageID: m.ID, LogicalMessageID: m.ID, ConnectionID: &c.ID, Channel: "sms", Status: status}
				require.NoError(t, f.db.Create(&a).Error)
				attempts = append(attempts, a)
			}
			if fail {
				failIntegrationCapture(t, f, audit.IntegrationChanged)
			}
			r := integrationAuditRequest(t, f, c.ID, map[string]any{"name": c.Name, "enabled": false, "max_segments": 3})
			require.NoError(t, f.app.UpdateChannelConnection(r))
			wantCode := 200
			if fail {
				wantCode = 503
			}
			require.Equal(t, wantCode, r.RequestCtx.Response.StatusCode())
			var retained models.ChannelConnection
			require.NoError(t, f.db.First(&retained, "id=?", c.ID).Error)
			require.Equal(t, fail, retained.Enabled)
			var queued, unknown models.ChannelAttempt
			require.NoError(t, f.db.First(&queued, "id=?", attempts[0].ID).Error)
			require.NoError(t, f.db.First(&unknown, "id=?", attempts[1].ID).Error)
			wantStatus := "cancelled"
			if fail {
				wantStatus = "queued"
			}
			require.Equal(t, wantStatus, queued.Status)
			require.Equal(t, "unknown", unknown.Status)
			rows := f.securityRows(t)
			if fail {
				require.Empty(t, rows)
			} else {
				require.Len(t, rows, 1)
				require.Equal(t, string(audit.Disabled), rows[0].Reason)
			}
			require.True(t, f.app.Config.Channels.StopDispatch)
		})
	}
}

func TestInboxAuditChannelDeletionAndValidationCaptureRollback(t *testing.T) {
	for _, mode := range []string{"delete", "delete_failure", "rotate", "rotate_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuditIntegrationFixture(t)
			p := &auditChannelProvider{}
			registry := channels.NewRegistry()
			require.NoError(t, registry.Register(p))
			f.app.Channels = &channelapp.Service{DB: f.db, Providers: registry, EncryptionKey: f.app.Config.App.EncryptionKey, StopDispatch: true}
			credentials, err := crypto.Encrypt(`{"client_id":"owned","client_secret":"synthetic-private-old"}`, f.app.Config.App.EncryptionKey)
			require.NoError(t, err)
			c := models.ChannelConnection{OrganizationID: f.org.ID, Name: "Synthetic channel", Provider: "audit_fake", MaxSegments: 3, Credentials: credentials, Capabilities: models.JSONB{}}
			require.NoError(t, f.db.Create(&c).Error)
			before := integrationRowJSON(t, f, "channel_connections", c.ID)
			failure := strings.HasSuffix(mode, "failure")
			if failure {
				kind := audit.IntegrationChanged
				if strings.HasPrefix(mode, "rotate") {
					kind = audit.IntegrationValidated
				}
				failIntegrationCapture(t, f, kind)
			}
			var r *fastglue.Request
			if strings.HasPrefix(mode, "delete") {
				r = integrationAuditRequest(t, f, c.ID, nil)
				require.NoError(t, f.app.DeleteChannelConnection(r))
			} else {
				r = integrationAuditRequest(t, f, c.ID, map[string]any{"name": c.Name, "enabled": false, "max_segments": 3, "credentials": map[string]string{"client_id": "owned", "client_secret": "synthetic-private-rotated"}})
				require.NoError(t, f.app.UpdateChannelConnection(r))
			}
			want := 200
			if failure {
				want = 503
			}
			require.Equal(t, want, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
			rows := f.securityRows(t)
			if failure {
				require.Empty(t, rows)
				require.Equal(t, before, integrationRowJSON(t, f, "channel_connections", c.ID))
			} else if mode == "delete" {
				require.Len(t, rows, 1)
				require.Equal(t, string(audit.Deleted), rows[0].Reason)
				var count int64
				require.NoError(t, f.db.Model(&models.ChannelConnection{}).Where("id=?", c.ID).Count(&count).Error)
				require.Zero(t, count)
			} else {
				require.Len(t, rows, 2)
				require.Equal(t, string(audit.CredentialRotated), rows[0].Reason)
				require.Equal(t, string(audit.IntegrationValidated), rows[1].Event)
			}
			require.Zero(t, p.sends.Load())
			require.True(t, f.app.Channels.StopDispatch)
			raw, err := json.Marshal(rows)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "synthetic-private-")
			require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
		})
	}
}

func TestInboxAuditIntegrationStorageErrorsNeverBecomeDiagnosticPayloads(t *testing.T) {
	f := newAuditIntegrationFixture(t)
	var logs auditLockedLog
	f.app.Log = logf.New(logf.Opts{Writer: &logs, Level: logf.ErrorLevel})
	r := integrationAuditRequest(t, f, uuid.Nil, nil)
	require.NoError(t, f.app.inboxError(r, errors.New("synthetic-private-credential in provider or database error")))
	require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
	require.Contains(t, logs.text(), "storage_unavailable")
	require.NotContains(t, logs.text(), "synthetic-private-")
	require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
}

func TestInboxAuditIntegrationCaptureFailureKeepsConfigurationOutOfSQLDebug(t *testing.T) {
	for _, spec := range auditIntegrationCases() {
		t.Run(spec.resource, func(t *testing.T) {
			f := newAuditIntegrationFixture(t)
			var logs auditLockedLog
			f.app.DB = f.db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Info})})
			var one int
			require.NoError(t, f.app.DB.Raw("SELECT 1").Scan(&one).Error)
			require.Equal(t, 1, one)
			require.Contains(t, logs.text(), "SELECT", "the debug logger must really be active")
			calls := failIntegrationCapture(t, f, audit.IntegrationChanged)
			r := integrationAuditRequest(t, f, uuid.Nil, spec.initial)
			require.NoError(t, spec.create(f.app, r))
			require.GreaterOrEqual(t, r.RequestCtx.Response.StatusCode(), 500)
			require.EqualValues(t, 1, calls.Load())
			require.NotContains(t, logs.text(), "synthetic-private-")
			require.Empty(t, f.securityRows(t))
		})
	}
}
