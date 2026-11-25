package handlers

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

func auditReaderFixture(t *testing.T, keys ...string) inboxAuditFixture {
	t.Helper()
	f := newInboxAuditAuthFixture(t)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "synthetic-audit-reader", keys)
	require.NoError(t, f.db.Model(f.user).Updates(map[string]any{"is_super_admin": false, "role_id": role.ID}).Error)
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", f.user.ID, f.org.ID).Update("role_id", role.ID).Error)
	f.user.IsSuperAdmin = false
	f.user.RoleID = &role.ID
	return f
}

func auditInvestigationRequest(t *testing.T, f inboxAuditFixture, params map[string]string) *fastglue.Request {
	t.Helper()
	r := testutil.NewGETRequest(t)
	testutil.SetFullAuthContext(r, f.org.ID, f.user.ID, f.user.RoleID, f.user.IsSuperAdmin)
	for key, value := range params {
		testutil.SetQueryParam(r, key, value)
	}
	return r
}

func auditOwnedContactRow(t *testing.T, f inboxAuditFixture, when time.Time) models.AuditLog {
	t.Helper()
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	row := models.AuditLog{OrganizationID: f.org.ID, ResourceType: "contact", ResourceID: contact.ID, UserID: f.user.ID, UserName: "Synthetic operator", Action: models.AuditActionUpdated, CreatedAt: when, Changes: models.JSONBArray{map[string]any{"field": "name", "new_value": "Synthetic"}}}
	require.NoError(t, f.db.Create(&row).Error)
	return row
}

func auditPage(t *testing.T, f inboxAuditFixture, params map[string]string) (*fastglue.Request, []AuditLogResponse, int64, string) {
	t.Helper()
	r := auditInvestigationRequest(t, f, params)
	require.NoError(t, f.app.ListAuditLogs(r))
	var response struct {
		Data struct {
			AuditLogs []AuditLogResponse `json:"audit_logs"`
			Total     int64              `json:"total"`
			Next      string             `json:"next_cursor"`
		}
	}
	if r.RequestCtx.Response.StatusCode() == 200 {
		require.NoError(t, json.Unmarshal(r.RequestCtx.Response.Body(), &response))
	}
	return r, response.Data.AuditLogs, response.Data.Total, response.Data.Next
}

func TestInboxAuditInvestigationPreservesExistingSettingsAndKeyActivityScopes(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "settings.general:read", "api_keys:read")
	key := models.APIKey{OrganizationID: f.org.ID, UserID: f.user.ID, Name: "Synthetic key target", KeyPrefix: "fixture", KeyHash: "not-a-real-credential"}
	require.NoError(t, f.db.Create(&key).Error)
	for _, resource := range []struct {
		kind string
		id   uuid.UUID
	}{{"api_key", key.ID}, {"settings.general", f.org.ID}, {"settings.calling", f.org.ID}} {
		row := models.AuditLog{OrganizationID: f.org.ID, ResourceType: resource.kind, ResourceID: resource.id, UserID: f.user.ID, UserName: "Synthetic operator", Action: models.AuditActionUpdated}
		require.NoError(t, f.db.Create(&row).Error)
	}
	_, rows, total, _ := auditPage(t, f, nil)
	require.Len(t, rows, 3)
	require.EqualValues(t, 3, total)
	for _, row := range rows {
		require.True(t, row.ResourceAvailable)
	}
	foreign := testutil.CreateTestOrganization(t, f.db)
	for _, kind := range []string{"settings.general", "settings.calling"} {
		row := models.AuditLog{OrganizationID: f.org.ID, ResourceType: kind, ResourceID: foreign.ID, UserID: f.user.ID, Action: models.AuditActionUpdated}
		require.NoError(t, f.db.Create(&row).Error)
	}
	_, rows, total, _ = auditPage(t, f, nil)
	require.Len(t, rows, 3)
	require.EqualValues(t, 3, total, "an organisation field alone cannot make an unrelated settings target readable")
}

func TestInboxAuditInvestigationHonoursFreshPhoneMaskingOnReadsAndCSV(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "audit_logs:export", "contacts:read")
	row := auditOwnedContactRow(t, f, time.Now().Add(-time.Hour))
	const phone = "+27820001234"
	require.NoError(t, f.db.Model(&row).Updates(map[string]any{"user_name": phone, "changes": models.JSONBArray{
		map[string]any{"field": "phone_number", "old_value": phone, "new_value": "[redacted]"},
		map[string]any{"field": "profile", "new_value": map[string]any{"phone_number": phone, "name": phone, "label": "Synthetic label"}},
	}}).Error)
	require.NoError(t, f.db.Model(&models.Organization{}).Where("id=?", f.org.ID).Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)
	r, rows, _, _ := auditPage(t, f, nil)
	require.Len(t, rows, 1)
	require.NotContains(t, string(r.RequestCtx.Response.Body()), phone)
	require.Contains(t, string(r.RequestCtx.Response.Body()), "1234")
	require.Contains(t, string(r.RequestCtx.Response.Body()), "[redacted]")
	detail := auditInvestigationRequest(t, f, nil)
	testutil.SetPathParam(detail, "id", row.ID.String())
	require.NoError(t, f.app.GetAuditLog(detail))
	require.NotContains(t, string(detail.RequestCtx.Response.Body()), phone)
	export := auditInvestigationRequest(t, f, map[string]string{"resource_type": "contact"})
	require.NoError(t, f.app.ExportAuditLogs(export))
	require.Equal(t, 200, export.RequestCtx.Response.StatusCode())
	require.NotContains(t, string(export.RequestCtx.Response.Body()), phone)
	var stored models.AuditLog
	require.NoError(t, f.db.First(&stored, "id=?", row.ID).Error)
	bytes, err := json.Marshal(stored.Changes)
	require.NoError(t, err)
	require.Contains(t, string(bytes), phone)
	require.NoError(t, f.db.Model(&models.Organization{}).Where("id=?", f.org.ID).Update("settings", models.JSONB{"mask_phone_numbers": false}).Error)
	r, _, _, _ = auditPage(t, f, map[string]string{"resource_type": "contact"})
	require.Contains(t, string(r.RequestCtx.Response.Body()), phone, "reads use current policy, not the previous cached setting")
}

func TestInboxAuditInvestigationUsesFreshRoleAndUnderlyingResourcePermission(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "contacts:read")
	row := auditOwnedContactRow(t, f, time.Now().Add(-time.Minute))
	require.True(t, f.app.HasPermission(f.user.ID, models.ResourceContacts, models.ActionRead, f.org.ID))
	r, rows, total, _ := auditPage(t, f, nil)
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Len(t, rows, 1)
	require.EqualValues(t, 1, total)
	var perm models.Permission
	require.NoError(t, f.db.First(&perm, "resource=? AND action=?", models.ResourceContacts, models.ActionRead).Error)
	require.NoError(t, f.db.Where("custom_role_id=? AND permission_id=?", *f.user.RoleID, perm.ID).Delete(&models.RolePermission{}).Error)
	r, rows, total, _ = auditPage(t, f, nil)
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Empty(t, rows)
	require.Zero(t, total)
	detail := auditInvestigationRequest(t, f, nil)
	testutil.SetPathParam(detail, "id", row.ID.String())
	require.NoError(t, f.app.GetAuditLog(detail))
	require.Equal(t, 404, detail.RequestCtx.Response.StatusCode())
	role := testutil.CreateTestRoleExact(t, f.db, f.org.ID, "replacement-no-audit", false, false, nil)
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", f.user.ID, f.org.ID).Update("role_id", role.ID).Error)
	r, _, _, _ = auditPage(t, f, nil)
	require.Equal(t, 403, r.RequestCtx.Response.StatusCode())
}

func TestInboxAuditInvestigationRefusesRevokedForeignOrForgedIdentity(t *testing.T) {
	for _, mode := range []string{"nil_membership_role", "deleted_membership", "foreign_role", "deleted_role", "disabled", "stamp", "claims_org", "flags"} {
		t.Run(mode, func(t *testing.T) {
			f := auditReaderFixture(t, "audit_logs:read", "contacts:read")
			auditOwnedContactRow(t, f, time.Now().Add(-time.Minute))
			r := auditInvestigationRequest(t, f, nil)
			want := 403
			switch mode {
			case "nil_membership_role":
				require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", f.user.ID, f.org.ID).Update("role_id", nil).Error)
			case "deleted_membership":
				require.NoError(t, f.db.Where("user_id=? AND organization_id=?", f.user.ID, f.org.ID).Delete(&models.UserOrganization{}).Error)
			case "foreign_role":
				other := testutil.CreateTestOrganization(t, f.db)
				role := testutil.CreateAdminRole(t, f.db, other.ID)
				require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", f.user.ID, f.org.ID).Update("role_id", role.ID).Error)
			case "deleted_role":
				require.NoError(t, f.db.Delete(&models.CustomRole{}, "id=?", *f.user.RoleID).Error)
			case "disabled":
				require.NoError(t, f.db.Model(f.user).Update("is_active", false).Error)
				want = 401
			case "stamp":
				r.RequestCtx.SetUserValue(middleware.ContextKeyClaims, &middleware.JWTClaims{UserID: f.user.ID, OrganizationID: f.org.ID, SessionID: "synthetic", AuthStamp: "stale"})
				want = 401
			case "claims_org":
				r.RequestCtx.SetUserValue(middleware.ContextKeyClaims, &middleware.JWTClaims{UserID: f.user.ID, OrganizationID: uuid.New()})
				want = 401
			case "flags":
				require.NoError(t, f.db.Where("custom_role_id=?", *f.user.RoleID).Delete(&models.RolePermission{}).Error)
				r.RequestCtx.SetUserValue("is_super_admin", true)
			}
			require.NoError(t, f.app.ListAuditLogs(r))
			require.Equal(t, want, r.RequestCtx.Response.StatusCode())
			require.NotContains(t, string(r.RequestCtx.Response.Body()), "audit_logs\":")
		})
	}
}

func auditCaseRecord(t *testing.T, f inboxAuditFixture, account *models.WhatsAppAccount, contact *models.Contact, assignee uuid.UUID, opened time.Time) models.InboxCase {
	t.Helper()
	c := models.InboxCase{OrganizationID: f.org.ID, AccountID: account.ID, ContactID: contact.ID, AssigneeID: &assignee, Status: models.InboxCaseOpen, Priority: models.InboxPriorityNormal, Version: 1, OpenedAt: opened, StatusChangedAt: opened}
	require.NoError(t, f.db.Create(&c).Error)
	context := f.event().Context
	context.AccountID = &account.ID
	context.CaseID = &c.ID
	context.Origin = audit.Origin{IP: "203.0.113.10", Source: "peer"}
	require.NoError(t, audit.RecordSecurity(f.db, audit.SecurityEvent{OrganizationID: f.org.ID, ActorID: assignee, ResourceType: "inbox_case", ResourceID: c.ID, Kind: audit.CaseChanged, Outcome: audit.Success, Reason: audit.StatusChanged, Context: context}))
	return c
}

func TestInboxAuditInvestigationScopesCurrentCaseAndAccountBeforeFiltersAndChoices(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "audit_logs:export", "chat:read")
	require.NoError(t, f.db.Model(f.org).Update("inbox_workflows_enabled", true).Error)
	other := testutil.CreateTestUser(t, f.db, f.org.ID)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	a := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	b := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	c := auditCaseRecord(t, f, a, contact, f.user.ID, time.Now().Add(-time.Hour))
	foreign := auditCaseRecord(t, f, b, contact, other.ID, time.Now().Add(-time.Hour))
	legacy := models.AuditLog{OrganizationID: f.org.ID, ResourceType: "contact", ResourceID: contact.ID, UserID: other.ID, UserName: "Hidden staff", Action: models.AuditActionUpdated}
	require.NoError(t, f.db.Create(&legacy).Error)
	r, rows, total, _ := auditPage(t, f, map[string]string{"limit": "1"})
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	require.Equal(t, c.ID, rows[0].ResourceID)
	r, rows, total, _ = auditPage(t, f, map[string]string{"case_id": foreign.ID.String()})
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Empty(t, rows)
	require.Zero(t, total)
	choices := auditInvestigationRequest(t, f, nil)
	require.NoError(t, f.app.ListAuditLogFilters(choices))
	require.Equal(t, 200, choices.RequestCtx.Response.StatusCode())
	require.NotContains(t, string(choices.RequestCtx.Response.Body()), "Hidden staff")
	require.NotContains(t, string(choices.RequestCtx.Response.Body()), other.ID.String())
	require.NoError(t, f.db.Model(&c).Updates(map[string]any{"status": "resolved", "resolved_at": time.Now()}).Error)
	auditCaseRecord(t, f, a, contact, other.ID, time.Now())
	r, rows, total, _ = auditPage(t, f, nil)
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Empty(t, rows)
	require.Zero(t, total, "former assignment cannot restore historical access")
}

func TestInboxAuditInvestigationLabelsLegacyAndRetainsVerifiedDeletedConfiguration(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "contacts:read", "settings.sso:read")
	legacy := auditOwnedContactRow(t, f, time.Now().Add(-time.Hour))
	require.NoError(t, f.db.Where("id=?", legacy.ResourceID).Delete(&models.Contact{}).Error)
	require.NoError(t, f.db.Model(&legacy).Updates(map[string]any{"outcome": "success", "origin_ip": "203.0.113.30", "request_id": "not-historical-proof"}).Error)
	unknown := models.AuditLog{OrganizationID: f.org.ID, ResourceType: "unknown", ResourceID: uuid.New(), UserID: f.user.ID, UserName: "Hidden unknown", Action: models.AuditActionCreated}
	require.NoError(t, f.db.Create(&unknown).Error)
	missing := unknown
	missing.ID = uuid.New()
	missing.ResourceType = "contact"
	require.NoError(t, f.db.Create(&missing).Error)
	provider := models.SSOProvider{OrganizationID: f.org.ID, Provider: "custom", ClientID: "synthetic", ClientSecret: "synthetic"}
	require.NoError(t, f.db.Create(&provider).Error)
	require.NoError(t, audit.RecordSecurity(f.db, audit.SecurityEvent{OrganizationID: f.org.ID, ActorID: f.user.ID, ResourceType: "sso_provider", ResourceID: provider.ID, Kind: audit.IntegrationChanged, Outcome: audit.Success, Reason: audit.Deleted, Context: f.event().Context}))
	require.NoError(t, f.db.Unscoped().Delete(&provider).Error)
	r, rows, total, _ := auditPage(t, f, nil)
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
	require.EqualValues(t, 2, total)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.False(t, row.ResourceAvailable)
		if row.ID == legacy.ID {
			require.False(t, row.ContextAvailable)
			require.Equal(t, "legacy", row.ContextState)
			require.Empty(t, row.Outcome)
			require.Empty(t, row.OriginIP)
			require.Empty(t, row.RequestID)
		} else {
			require.True(t, row.ContextAvailable)
		}
	}
}

func TestInboxAuditInvestigationMasksActorAndOriginWithoutDirectoryAccess(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "accounts:read")
	account := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	other := testutil.CreateTestUser(t, f.db, f.org.ID)
	context := f.event().Context
	context.AccountID = &account.ID
	context.Origin = audit.Origin{IP: "203.0.113.44", Source: "peer"}
	require.NoError(t, audit.RecordSecurity(f.db, audit.SecurityEvent{OrganizationID: f.org.ID, ActorID: other.ID, ResourceType: "account", ResourceID: account.ID, Kind: audit.IntegrationChanged, Outcome: audit.Success, Reason: audit.ConfigurationChanged, Context: context}))
	r, rows, _, _ := auditPage(t, f, nil)
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Len(t, rows, 1)
	require.True(t, rows[0].ActorMasked)
	require.False(t, rows[0].ActorAvailable)
	require.Equal(t, uuid.Nil, rows[0].UserID)
	require.Equal(t, "Staff member", rows[0].UserName)
	require.True(t, rows[0].OriginMasked)
	require.Empty(t, rows[0].OriginIP)
	choices := auditInvestigationRequest(t, f, nil)
	require.NoError(t, f.app.ListAuditLogFilters(choices))
	require.NotContains(t, string(choices.RequestCtx.Response.Body()), other.ID.String())
	require.NotContains(t, string(r.RequestCtx.Response.Body()), other.FullName)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "directory-audit", []string{"audit_logs:read", "accounts:read", "users:read"})
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", f.user.ID, f.org.ID).Update("role_id", role.ID).Error)
	_, rows, _, _ = auditPage(t, f, nil)
	require.Len(t, rows, 1)
	require.True(t, rows[0].ActorAvailable)
	require.NoError(t, f.db.Delete(&other).Error)
	_, rows, _, _ = auditPage(t, f, nil)
	require.Len(t, rows, 1)
	require.False(t, rows[0].ActorAvailable, "a recorded actor label is not proof of a live directory target")
}

func TestInboxAuditInvestigationStrictFiltersStablePagingAndCompleteDateBoundaries(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "contacts:read")
	when := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	first := auditOwnedContactRow(t, f, when.Add(time.Hour))
	second := auditOwnedContactRow(t, f, when.Add(24*time.Hour-time.Microsecond))
	auditOwnedContactRow(t, f, when.Add(24*time.Hour))
	r, rows, total, _ := auditPage(t, f, map[string]string{"from": when.Format(time.DateOnly), "to": when.Format(time.DateOnly)})
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Len(t, rows, 2)
	require.EqualValues(t, 2, total)
	for _, params := range []map[string]string{{"resource_id": "bad"}, {"user_id": "bad"}, {"from": "bad"}, {"to": "bad"}, {"page": "0"}, {"page": "99999999999999999999999"}, {"limit": "101"}, {"outcome": "made-up"}, {"resource_type": "unknown"}, {"cursor": "bad"}, {"extra": "ignored-before"}} {
		r, _, _, _ := auditPage(t, f, params)
		require.Equal(t, 400, r.RequestCtx.Response.StatusCode(), params)
	}
	// Exact timestamp ties use record UUID order, not an unstable insertion order.
	require.NoError(t, f.db.Model(&models.AuditLog{}).Where("id IN ?", []uuid.UUID{first.ID, second.ID}).Update("created_at", when).Error)
	r, rows, total, cursor := auditPage(t, f, map[string]string{"limit": "2"})
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.EqualValues(t, 3, total)
	require.Len(t, rows, 2)
	require.NotEmpty(t, cursor)
	var start struct {
		Data struct {
			First string `json:"first_cursor"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(r.RequestCtx.Response.Body(), &start))
	require.NotEmpty(t, start.Data.First)
	seen := map[uuid.UUID]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	auditOwnedContactRow(t, f, time.Now())
	r, rows, total, next := auditPage(t, f, map[string]string{"limit": "2", "cursor": cursor, "action": ""})
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Len(t, rows, 1)
	require.EqualValues(t, 3, total)
	require.Empty(t, next)
	require.False(t, seen[rows[0].ID])
	r, rows, total, _ = auditPage(t, f, map[string]string{"limit": "2", "cursor": start.Data.First})
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.EqualValues(t, 3, total, "returning to the first page keeps the original snapshot")
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.True(t, seen[row.ID], "a new arrival cannot move the first page during navigation")
	}
	r, _, _, _ = auditPage(t, f, map[string]string{"cursor": cursor, "resource_type": "contact"})
	require.Equal(t, 400, r.RequestCtx.Response.StatusCode(), "cursor must bind filters")
	duplicate := auditInvestigationRequest(t, f, nil)
	duplicate.RequestCtx.QueryArgs().Add("limit", "1")
	duplicate.RequestCtx.QueryArgs().Add("limit", "2")
	require.NoError(t, f.app.ListAuditLogs(duplicate))
	require.Equal(t, 400, duplicate.RequestCtx.Response.StatusCode())
}

func TestInboxAuditInvestigationStorageFailuresAreRecoverableNotEmptyOrNotFound(t *testing.T) {
	for _, phase := range []string{"count", "rows"} {
		t.Run(phase, func(t *testing.T) {
			f := auditReaderFixture(t, "audit_logs:read", "contacts:read")
			entry := auditOwnedContactRow(t, f, time.Now().Add(-time.Minute))
			name := "audit_read_fault_" + uuid.NewString()
			callback := func(db *gorm.DB) {
				if db.Statement.Table == "audit_logs" {
					_ = db.AddError(errors.New("synthetic-private-storage-error"))
				}
			}
			if phase == "count" {
				require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register(name, callback))
				t.Cleanup(func() { require.NoError(t, f.db.Callback().Query().Remove(name)) })
			} else {
				require.NoError(t, f.db.Callback().Row().Before("gorm:row").Register(name, callback))
				t.Cleanup(func() { require.NoError(t, f.db.Callback().Row().Remove(name)) })
			}
			r, _, _, _ := auditPage(t, f, nil)
			require.Equal(t, 503, r.RequestCtx.Response.StatusCode())
			require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
			if phase == "rows" {
				r = auditInvestigationRequest(t, f, nil)
				testutil.SetPathParam(r, "id", entry.ID.String())
				require.NoError(t, f.app.GetAuditLog(r))
				require.Equal(t, 503, r.RequestCtx.Response.StatusCode())
			}
			if phase == "count" {
				require.NoError(t, f.db.Callback().Query().Remove(name))
			} else {
				require.NoError(t, f.db.Callback().Row().Remove(name))
			}
			r, rows, _, _ := auditPage(t, f, nil)
			require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
			require.Len(t, rows, 1)
		})
	}
}

func TestInboxAuditExportUsesSameScopeEscapesCSVAndPagesWithoutRepeats(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "audit_logs:export", "contacts:read", "users:read")
	when := time.Now().Add(-time.Hour)
	ids := []string{}
	for i := 0; i < 3; i++ {
		row := auditOwnedContactRow(t, f, when)
		ids = append(ids, row.ID.String())
		require.NoError(t, f.db.Model(&row).Updates(map[string]any{"user_name": "\t =HYPERLINK(\"example.invalid\")", "changes": models.JSONBArray{map[string]any{"field": "api_key", "new_value": "synthetic-private-credential"}}}).Error)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	r := auditInvestigationRequest(t, f, map[string]string{"limit": "2"})
	r.RequestCtx.Request.Header.SetMethod("POST")
	r.RequestCtx.Request.SetRequestURI("/api/audit-logs/export?limit=2")
	require.NotNil(t, f.app.AuthorizeAPI()(r), "fresh export gate must not require nonexistent audit write permission")
	require.NoError(t, f.app.ExportAuditLogs(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
	require.Equal(t, "no-store", string(r.RequestCtx.Response.Header.Peek("Cache-Control")))
	require.Equal(t, "true", string(r.RequestCtx.Response.Header.Peek("X-Audit-Has-More")))
	parse := func(r *fastglue.Request) [][]string {
		data := strings.TrimPrefix(string(r.RequestCtx.Response.Body()), "\ufeff")
		rows, err := csv.NewReader(strings.NewReader(data)).ReadAll()
		require.NoError(t, err)
		return rows
	}
	values := parse(r)
	require.Len(t, values, 3)
	require.Equal(t, ids[0], values[1][1])
	require.True(t, strings.HasPrefix(values[1][4], "'\t ="))
	require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
	cursor := string(r.RequestCtx.Response.Header.Peek("X-Audit-Next-Cursor"))
	require.NotEmpty(t, cursor)
	next := auditInvestigationRequest(t, f, map[string]string{"limit": "2", "cursor": cursor})
	require.NoError(t, f.app.ExportAuditLogs(next))
	require.Equal(t, 200, next.RequestCtx.Response.StatusCode())
	values = parse(next)
	require.Len(t, values, 2)
	require.Equal(t, ids[2], values[1][1])
	require.Equal(t, "false", string(next.RequestCtx.Response.Header.Peek("X-Audit-Has-More")))
	var events []models.AuditLog
	require.NoError(t, f.db.Where("organization_id=? AND event=?", f.org.ID, string(audit.Exported)).Find(&events).Error)
	require.Len(t, events, 2)
	for _, event := range events {
		require.Equal(t, string(audit.ExportPrepared), event.Reason)
		require.Empty(t, event.Changes)
	}
}

func TestInboxAuditExportRefusesMissingPermissionRevocationAndCaptureFailure(t *testing.T) {
	for _, mode := range []string{"permission", "revoked_during_read", "case_reassigned", "phone_policy_changed", "capture_failure"} {
		t.Run(mode, func(t *testing.T) {
			keys := []string{"audit_logs:read", "contacts:read"}
			if mode != "permission" {
				keys = append(keys, "audit_logs:export")
			}
			f := auditReaderFixture(t, keys...)
			var caseID uuid.UUID
			if mode == "case_reassigned" {
				role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "audit-case-exporter", []string{"audit_logs:read", "audit_logs:export", "chat:read"})
				require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", f.user.ID, f.org.ID).Update("role_id", role.ID).Error)
				require.NoError(t, f.db.Model(f.org).Update("inbox_workflows_enabled", true).Error)
				account := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
				contact := testutil.CreateTestContact(t, f.db, f.org.ID)
				c := auditCaseRecord(t, f, account, contact, f.user.ID, time.Now().Add(-time.Hour))
				caseID = c.ID
			} else {
				auditOwnedContactRow(t, f, time.Now().Add(-time.Minute))
			}
			if mode == "capture_failure" {
				failIntegrationCapture(t, f, audit.Exported)
			}
			if mode == "revoked_during_read" || mode == "case_reassigned" || mode == "phone_policy_changed" {
				var once atomic.Bool
				name := "audit_export_revoke_" + uuid.NewString()
				require.NoError(t, f.db.Callback().Row().After("gorm:row").Register(name, func(db *gorm.DB) {
					if db.Statement.Table != "audit_logs" || once.Swap(true) {
						return
					}
					if mode == "phone_policy_changed" {
						require.NoError(t, f.db.Model(&models.Organization{}).Where("id=?", f.org.ID).Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)
						return
					}
					if mode == "case_reassigned" {
						other := testutil.CreateTestUser(t, f.db, f.org.ID)
						require.NoError(t, f.db.Model(&models.InboxCase{}).Where("id=?", caseID).Update("assignee_id", other.ID).Error)
						return
					}
					var permission models.Permission
					require.NoError(t, f.db.First(&permission, "resource=? AND action=?", models.ResourceAuditLogs, models.ActionExport).Error)
					require.NoError(t, f.db.Where("custom_role_id=? AND permission_id=?", *f.user.RoleID, permission.ID).Delete(&models.RolePermission{}).Error)
				}))
				t.Cleanup(func() { require.NoError(t, f.db.Callback().Row().Remove(name)) })
			}
			r := auditInvestigationRequest(t, f, nil)
			require.NoError(t, f.app.ExportAuditLogs(r))
			want := 403
			if mode == "capture_failure" {
				want = 503
			}
			if mode == "case_reassigned" || mode == "phone_policy_changed" {
				want = 409
			}
			require.Equal(t, want, r.RequestCtx.Response.StatusCode())
			require.NotContains(t, string(r.RequestCtx.Response.Header.Peek("Content-Type")), "text/csv")
			var count int64
			require.NoError(t, f.db.Model(&models.AuditLog{}).Where("organization_id=? AND event=?", f.org.ID, string(audit.Exported)).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestInboxAuditCSVCellAndOutputBounds(t *testing.T) {
	for _, value := range []string{"=1+1", " +1", "\t-2", "\r@SUM(A1)", "\ufeff=1", "\u0001 +1"} {
		require.Equal(t, "'"+value, auditCSVCell(value))
	}
	require.Equal(t, "ordinary text", auditCSVCell("ordinary text"))
	f := auditReaderFixture(t, "audit_logs:read", "audit_logs:export", "contacts:read")
	for _, params := range []map[string]string{{"limit": "1001"}, {"page": "1"}, {"cursor": "not-a-cursor"}} {
		r := auditInvestigationRequest(t, f, params)
		require.NoError(t, f.app.ExportAuditLogs(r))
		require.Equal(t, 400, r.RequestCtx.Response.StatusCode())
	}
	viewer, err := loadAuditViewer(f.db, f.org.ID, f.user.ID, nil)
	require.NoError(t, err)
	changes := models.JSONBArray{}
	for i := 0; i < 8; i++ {
		changes = append(changes, map[string]any{"field": "description", "new_value": strings.Repeat("x", 2048)})
	}
	row := auditReadRow{AuditLog: models.AuditLog{ID: uuid.New(), OrganizationID: f.org.ID, ResourceType: "contact", ResourceID: uuid.New(), UserID: f.user.ID, UserName: "Synthetic", Changes: changes, CreatedAt: time.Now()}}
	large := make([]auditReadRow, 1000)
	for i := range large {
		large[i] = row
	}
	_, err = encodeAuditCSV(large, viewer)
	require.ErrorIs(t, err, errAuditExportSize)
}

func TestInboxAuditInvestigationIndexesAndRepresentativeQueryBudget(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "contacts:read")
	require.True(t, f.db.Migrator().HasIndex(&models.AuditLog{}, "idx_audit_org_time"))
	require.True(t, f.db.Migrator().HasIndex(&models.AuditLog{}, "idx_audit_org_actor_time"))
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	rows := make([]models.AuditLog, 2000)
	for i := range rows {
		rows[i] = models.AuditLog{ID: uuid.New(), OrganizationID: f.org.ID, ResourceType: "contact", ResourceID: contact.ID, UserID: f.user.ID, UserName: "Synthetic", Action: models.AuditActionUpdated, CreatedAt: time.Now().Add(-time.Duration(i+1) * time.Minute)}
	}
	require.NoError(t, f.db.CreateInBatches(rows, 200).Error)
	started := time.Now()
	r, visible, total, cursor := auditPage(t, f, map[string]string{"limit": "50"})
	elapsed := time.Since(started)
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Len(t, visible, 50)
	require.EqualValues(t, 2000, total)
	require.NotEmpty(t, cursor)
	require.Less(t, elapsed, 3*time.Second)
	var definition string
	require.NoError(t, f.db.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND indexname='idx_audit_org_time'").Scan(&definition).Error)
	require.Contains(t, definition, "organization_id, created_at DESC, id DESC")
	var plan []struct {
		Plan string `gorm:"column:QUERY PLAN"`
	}
	require.NoError(t, f.db.Raw("EXPLAIN (ANALYZE,BUFFERS) SELECT id FROM audit_logs WHERE organization_id=? ORDER BY created_at DESC,id DESC LIMIT 50", f.org.ID).Scan(&plan).Error)
	require.NotEmpty(t, plan)
	t.Logf("2000-record scoped audit count/page=%s; indexed paging plan=%v", elapsed, plan)
}

func TestInboxAuditInvestigationBoundsHistoricalDiffsBeforeDecoding(t *testing.T) {
	f := auditReaderFixture(t, "audit_logs:read", "contacts:read")
	row := auditOwnedContactRow(t, f, time.Now().Add(-time.Minute))
	changes := models.JSONBArray{map[string]any{"field": "old_description", "new_value": strings.Repeat("synthetic-private-large-value", 5000)}}
	require.NoError(t, f.db.Model(&row).Update("changes", changes).Error)
	r, rows, _, _ := auditPage(t, f, nil)
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.Len(t, rows, 1)
	require.Contains(t, string(r.RequestCtx.Response.Body()), "changes_unavailable")
	require.Less(t, len(r.RequestCtx.Response.Body()), 4096)
	require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
	var original string
	require.NoError(t, f.db.Raw("SELECT changes::text FROM audit_logs WHERE id=?", row.ID).Scan(&original).Error)
	require.Greater(t, len(original), 65536, "reading must not rewrite stored history")
}
