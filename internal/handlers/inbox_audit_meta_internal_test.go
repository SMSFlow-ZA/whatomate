package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type auditMetaFixture struct {
	inboxAuditFixture
	phone, business                         string
	registrations, subscriptions, exchanges atomic.Int32
	beforeRegistration                      func()
	beforePhoneInfo                         func(context.Context)
	rejectRegistration, rejectExchange      bool
	logs                                    auditLockedLog
}

func newAuditMetaFixture(t *testing.T) *auditMetaFixture {
	t.Helper()
	m := &auditMetaFixture{inboxAuditFixture: newAuditIntegrationFixture(t), phone: uuid.NewString(), business: uuid.NewString()}
	m.app.Log = logf.New(logf.Opts{Writer: &m.logs, Level: logf.DebugLevel})
	m.app.DB = m.db.Session(&gorm.Session{Logger: logger.New(log.New(&m.logs, "", 0), logger.Config{LogLevel: logger.Info})})
	m.app.Config.WhatsApp.AppID = "synthetic-meta-app"
	m.app.Config.WhatsApp.AppSecret = "synthetic-private-meta-secret"
	m.app.Config.WhatsApp.APIVersion = "v21.0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/access_token"):
			m.exchanges.Add(1)
			if m.rejectExchange {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":100,"message":"synthetic-private-code-secret"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"synthetic-private-new-token"}`))
		case strings.HasSuffix(r.URL.Path, "/"+m.phone+"/register"):
			m.registrations.Add(1)
			if m.beforeRegistration != nil {
				m.beforeRegistration()
			}
			if m.rejectRegistration {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":368,"message":"synthetic-private-provider-rejection"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		case strings.HasSuffix(r.URL.Path, "/"+m.phone):
			if m.beforePhoneInfo != nil {
				m.beforePhoneInfo(r.Context())
			}
			_, _ = w.Write([]byte(`{"verified_name":"Synthetic business","display_phone_number":"+27820000000"}`))
		case strings.HasSuffix(r.URL.Path, "/"+m.business+"/subscribed_apps"):
			m.subscriptions.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	m.app.WhatsApp = whatsapp.NewWithBaseURL(m.app.Log, server.URL)
	return m
}

func (m *auditMetaFixture) existing(t *testing.T) models.WhatsAppAccount {
	t.Helper()
	a := models.WhatsAppAccount{OrganizationID: m.org.ID, Name: "Synthetic existing " + uuid.NewString(), PhoneID: m.phone, BusinessID: m.business, AccessToken: "synthetic-private-original-token", AppSecret: "synthetic-private-original-secret", WebhookVerifyToken: "synthetic-private-verify", Pin: "984731", Status: "pending_registration", APIVersion: "v21.0"}
	require.NoError(t, m.app.encryptAccountSecrets(&a))
	require.NoError(t, m.db.Create(&a).Error)
	require.NoError(t, m.db.First(&a, "id=?", a.ID).Error)
	return a
}

func (m *auditMetaFixture) exchangeRequest(t *testing.T) *fastglue.Request {
	t.Helper()
	return integrationAuditRequest(t, m.inboxAuditFixture, uuid.Nil, map[string]any{"code": "synthetic-private-signup-code", "phone_id": m.phone, "waba_id": m.business, "name": "Synthetic onboarding " + uuid.NewString(), "webhook_verify_token": "synthetic-private-new-verify"})
}

func (m *auditMetaFixture) assertPrivate(t *testing.T) {
	t.Helper()
	rows := m.securityRows(t)
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "synthetic-private-")
	require.NotContains(t, string(raw), "984731")
	require.NotContains(t, m.logs.text(), "synthetic-private-")
	require.NotContains(t, m.logs.text(), "984731")
	require.True(t, m.app.Config.Channels.StopDispatch)
	require.False(t, m.app.Config.InboxAutomation.DispatchEnabled)
	require.False(t, m.app.Config.InboxAutomation.IntegrationDispatchEnabled)
}

func TestInboxAuditEmbeddedSignupRecordsDurablePhasesAndNeverLocksAcrossMeta(t *testing.T) {
	for _, mode := range []string{"created", "pending", "create_capture_failure", "final_capture_failure", "concurrent_change", "concurrent_delete", "existing", "existing_capture_failure", "archived", "exchange_rejected"} {
		t.Run(mode, func(t *testing.T) {
			m := newAuditMetaFixture(t)
			var existing models.WhatsAppAccount
			if strings.HasPrefix(mode, "existing") || mode == "archived" {
				existing = m.existing(t)
				if mode == "archived" {
					require.NoError(t, m.db.Delete(&existing).Error)
				}
			}
			before := ""
			if existing.ID != uuid.Nil {
				before = integrationRowJSON(t, m.inboxAuditFixture, "whatsapp_accounts", existing.ID)
			}
			if mode == "create_capture_failure" {
				failIntegrationCapture(t, m.inboxAuditFixture, audit.IntegrationChanged)
			}
			if mode == "final_capture_failure" || mode == "existing_capture_failure" {
				failIntegrationCapture(t, m.inboxAuditFixture, audit.RegistrationUpdated)
			}
			m.rejectRegistration = mode == "pending"
			m.rejectExchange = mode == "exchange_rejected"
			m.beforeRegistration = func() {
				// The reserved identity is committed, but no DB lock spans the Meta call.
				require.NoError(t, m.db.Transaction(func(tx *gorm.DB) error {
					var row models.WhatsAppAccount
					return tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&row, "organization_id=? AND phone_id=?", m.org.ID, m.phone).Error
				}))
				if existing.ID == uuid.Nil {
					rows := m.securityRows(t)
					require.Len(t, rows, 1)
					require.Equal(t, string(audit.Created), rows[0].Reason)
				}
				if mode == "concurrent_change" {
					require.NoError(t, m.db.Model(&models.WhatsAppAccount{}).Where("organization_id=? AND phone_id=?", m.org.ID, m.phone).Update("name", "Concurrent operator update").Error)
				}
				if mode == "concurrent_delete" {
					require.NoError(t, m.db.Where("organization_id=? AND phone_id=?", m.org.ID, m.phone).Delete(&models.WhatsAppAccount{}).Error)
				}
			}
			r := m.exchangeRequest(t)
			require.NoError(t, m.app.ExchangeToken(r))
			want := 200
			switch mode {
			case "create_capture_failure":
				want = 500
			case "final_capture_failure", "existing_capture_failure":
				want = 503
			case "concurrent_change", "concurrent_delete", "archived":
				want = 409
			case "exchange_rejected":
				want = 400
			}
			require.Equal(t, want, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
			rows := m.securityRows(t)
			if mode == "create_capture_failure" || mode == "archived" || mode == "exchange_rejected" {
				require.Empty(t, rows)
				require.Zero(t, m.registrations.Load())
				require.Zero(t, m.subscriptions.Load())
			} else {
				require.EqualValues(t, 1, m.registrations.Load())
				require.EqualValues(t, 1, m.subscriptions.Load())
				switch mode {
				case "created", "pending", "existing":
					require.Len(t, rows, 2)
					byKind := map[string]models.AuditLog{}
					for _, row := range rows {
						byKind[row.Event] = row
					}
					require.Len(t, byKind, 2)
					reasonChanged := audit.Created
					if mode == "existing" {
						reasonChanged = audit.CredentialRotated
					}
					require.Equal(t, string(reasonChanged), byKind[string(audit.IntegrationChanged)].Reason)
					reason := audit.RegistrationActive
					if mode == "pending" {
						reason = audit.RegistrationPending
					}
					require.Equal(t, string(reason), byKind[string(audit.RegistrationUpdated)].Reason)
					for _, row := range rows {
						require.NotNil(t, row.AccountID)
						require.Equal(t, row.ResourceID, *row.AccountID)
						require.True(t, row.ActorVerified)
					}
				case "existing_capture_failure":
					require.Empty(t, rows)
					require.Equal(t, before, integrationRowJSON(t, m.inboxAuditFixture, "whatsapp_accounts", existing.ID))
				default:
					require.Len(t, rows, 1)
					require.Equal(t, string(audit.Created), rows[0].Reason)
				}
			}
			var stored []models.WhatsAppAccount
			require.NoError(t, m.db.Unscoped().Where("organization_id=? AND phone_id=?", m.org.ID, m.phone).Find(&stored).Error)
			if mode == "create_capture_failure" || mode == "exchange_rejected" {
				require.Empty(t, stored)
			} else {
				require.Len(t, stored, 1)
				require.True(t, crypto.IsEncrypted(stored[0].AccessToken))
				require.True(t, crypto.IsEncrypted(stored[0].AppSecret))
				if mode == "final_capture_failure" {
					require.Equal(t, "pending_registration", stored[0].Status)
					require.Empty(t, stored[0].Pin)
				}
				if mode == "concurrent_change" {
					require.Equal(t, "Concurrent operator update", stored[0].Name)
				}
				if mode == "concurrent_delete" || mode == "archived" {
					require.True(t, stored[0].DeletedAt.Valid)
				}
			}
			var response map[string]any
			require.NoError(t, json.Unmarshal(r.RequestCtx.Response.Body(), &response))
			if want == 200 {
				// This write-authorized setup response intentionally exposes its
				// webhook verification token and generated PIN, as before.
				data := response["data"].(map[string]any)
				account := data["account"].(map[string]any)
				require.Equal(t, "synthetic-private-new-verify", account["webhook_verify_token"])
				delete(account, "webhook_verify_token")
			}
			redacted, err := json.Marshal(response)
			require.NoError(t, err)
			require.NotContains(t, string(redacted), "synthetic-private-")
			m.assertPrivate(t)
		})
	}
}

func TestInboxAuditManualMetaRegistrationPreservesPriorStateOnFailure(t *testing.T) {
	for _, mode := range []string{"success", "capture_failure", "concurrent_change", "provider_failure"} {
		t.Run(mode, func(t *testing.T) {
			m := newAuditMetaFixture(t)
			existing := m.existing(t)
			before := integrationRowJSON(t, m.inboxAuditFixture, "whatsapp_accounts", existing.ID)
			if mode == "capture_failure" {
				failIntegrationCapture(t, m.inboxAuditFixture, audit.RegistrationUpdated)
			}
			m.rejectRegistration = mode == "provider_failure"
			m.beforeRegistration = func() {
				require.NoError(t, m.db.Transaction(func(tx *gorm.DB) error {
					var row models.WhatsAppAccount
					return tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&row, "id=?", existing.ID).Error
				}))
				if mode == "concurrent_change" {
					require.NoError(t, m.db.Model(&models.WhatsAppAccount{}).Where("id=?", existing.ID).Update("name", "Winning operator change").Error)
				}
			}
			r := integrationAuditRequest(t, m.inboxAuditFixture, existing.ID, map[string]string{"pin": "984731"})
			require.NoError(t, m.app.RegisterPhoneNumber(r))
			require.EqualValues(t, 1, m.registrations.Load())
			require.Zero(t, m.subscriptions.Load())
			want := 200
			switch mode {
			case "capture_failure":
				want = 503
			case "concurrent_change":
				want = 409
			case "provider_failure":
				want = 400
			}
			require.Equal(t, want, r.RequestCtx.Response.StatusCode())
			rows := m.securityRows(t)
			if mode == "success" {
				require.Len(t, rows, 1)
				require.Equal(t, string(audit.RegistrationUpdated), rows[0].Event)
				require.Equal(t, string(audit.RegistrationActive), rows[0].Reason)
			} else {
				require.Empty(t, rows)
			}
			if mode == "capture_failure" || mode == "provider_failure" {
				require.Equal(t, before, integrationRowJSON(t, m.inboxAuditFixture, "whatsapp_accounts", existing.ID))
			}
			if mode == "concurrent_change" {
				var retained models.WhatsAppAccount
				require.NoError(t, m.db.First(&retained, "id=?", existing.ID).Error)
				require.Equal(t, "Winning operator change", retained.Name)
				require.Equal(t, "pending_registration", retained.Status)
			}
			require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
			m.assertPrivate(t)
		})
	}
}

func TestInboxAuditMetaOnboardingRejectsAgentBeforeAnyProviderCall(t *testing.T) {
	m := newAuditMetaFixture(t)
	existing := m.existing(t)
	role := testutil.CreateAgentRole(t, m.db, m.org.ID)
	u := testutil.CreateTestUser(t, m.db, m.org.ID, testutil.WithRoleID(&role.ID))
	requests := []*fastglue.Request{m.exchangeRequest(t), integrationAuditRequest(t, m.inboxAuditFixture, existing.ID, map[string]string{"pin": "984731"})}
	for i, r := range requests {
		testutil.SetFullAuthContext(r, m.org.ID, u.ID, u.RoleID, false)
		if i == 0 {
			require.NoError(t, m.app.ExchangeToken(r))
		} else {
			require.NoError(t, m.app.RegisterPhoneNumber(r))
		}
		require.Equal(t, 403, r.RequestCtx.Response.StatusCode())
	}
	require.Zero(t, m.exchanges.Load())
	require.Zero(t, m.registrations.Load())
	require.Zero(t, m.subscriptions.Load())
	for _, row := range m.securityRows(t) {
		require.Equal(t, string(audit.AuthorizationDenied), row.Event)
	}
	m.assertPrivate(t)
}

func TestInboxAuditConcurrentEmbeddedSignupReservesOneAccountBeforeRemoteWrites(t *testing.T) {
	m := newAuditMetaFixture(t)
	ready := make(chan struct{})
	var arrivals atomic.Int32
	m.beforePhoneInfo = func(ctx context.Context) {
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-ctx.Done():
		}
	}
	requests := []*fastglue.Request{m.exchangeRequest(t), m.exchangeRequest(t)}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, r := range requests {
		wg.Add(1)
		go func(r *fastglue.Request) { defer wg.Done(); <-start; results <- m.app.ExchangeToken(r) }(r)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	codes := []int{requests[0].RequestCtx.Response.StatusCode(), requests[1].RequestCtx.Response.StatusCode()}
	sort.Ints(codes)
	require.Equal(t, []int{200, 409}, codes)
	var count int64
	require.NoError(t, m.db.Model(&models.WhatsAppAccount{}).Where("organization_id=? AND phone_id=?", m.org.ID, m.phone).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.EqualValues(t, 1, m.registrations.Load())
	require.EqualValues(t, 1, m.subscriptions.Load())
	rows := m.securityRows(t)
	require.Len(t, rows, 2)
	m.assertPrivate(t)
}
