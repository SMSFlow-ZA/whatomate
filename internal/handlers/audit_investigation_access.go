package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

var (
	errAuditUnauthenticated = errors.New("audit identity unavailable")
	errAuditForbidden       = errors.New("audit access denied")
	errAuditInput           = errors.New("invalid audit filters")
	errAuditChanged         = errors.New("audit access or results changed")
	errAuditExportSize      = errors.New("audit export page exceeds size limit")
	errAuditStorage         = errors.New("audit storage unavailable")
)

type auditViewer struct {
	Org, User        uuid.UUID
	Role, Membership *uuid.UUID
	Stamp            string
	Super            bool
	Permissions      map[string]bool
}

func (v auditViewer) has(resource, action string) bool {
	return v.Super || v.Permissions[resource+":"+action]
}
func (v auditViewer) fingerprint() string {
	keys := make([]string, 0, len(v.Permissions))
	for key := range v.Permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	b, _ := json.Marshal([]any{v.Org, v.User, v.Role, v.Membership, v.Stamp, v.Super, keys})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Audit authority is always loaded from PostgreSQL. A removed/nil membership
// cannot inherit a native role, and neither Redis nor request role flags grant it.
func loadAuditViewer(db *gorm.DB, org, user uuid.UUID, claims *middleware.JWTClaims) (auditViewer, error) {
	v := auditViewer{Org: org, User: user, Permissions: map[string]bool{}}
	if db == nil {
		return v, errAuditStorage
	}
	if org == uuid.Nil || user == uuid.Nil {
		return v, errAuditUnauthenticated
	}
	if claims != nil && (claims.UserID != user || claims.OrganizationID != org) {
		return v, errAuditUnauthenticated
	}
	var person models.User
	if err := db.Select("id,organization_id,role_id,is_active,is_super_admin,auth_stamp").First(&person, "id=? AND is_active", user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return v, errAuditUnauthenticated
		}
		return v, err
	}
	if claims != nil && claims.SessionID != "" && claims.AuthStamp != person.AuthStamp {
		return v, errAuditUnauthenticated
	}
	var organization models.Organization
	if err := db.Select("id").First(&organization, "id=?", org).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return v, errAuditForbidden
		}
		return v, err
	}
	v.Stamp, v.Super = person.AuthStamp, person.IsSuperAdmin
	if v.Super {
		return v, nil
	}
	var member models.UserOrganization
	err := db.Unscoped().First(&member, "user_id=? AND organization_id=?", user, org).Error
	if err == nil {
		if member.DeletedAt.Valid {
			return v, errAuditForbidden
		}
		v.Membership, v.Role = &member.ID, member.RoleID
	} else if errors.Is(err, gorm.ErrRecordNotFound) && person.OrganizationID == org {
		v.Role = person.RoleID // Supported legacy native identity without a membership row.
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		return v, errAuditForbidden
	} else {
		return v, err
	}
	if v.Role == nil {
		return v, nil
	}
	var role models.CustomRole
	if err := db.Select("id").First(&role, "id=? AND organization_id=?", *v.Role, org).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return v, errAuditForbidden
		}
		return v, err
	}
	var perms []models.Permission
	if err := db.Model(&models.Permission{}).Joins("JOIN role_permissions rp ON rp.permission_id=permissions.id").Where("rp.custom_role_id=?", role.ID).Select("permissions.resource,permissions.action").Find(&perms).Error; err != nil {
		return v, err
	}
	for _, p := range perms {
		v.Permissions[p.Resource+":"+p.Action] = true
	}
	return v, nil
}

type auditResourceSpec struct {
	Type, Permission, Action, Table, Target string
	Trusted                                 []audit.EventKind
}

// Only static server-owned resource/table names may enter these SQL fragments.
// A missing target can use verified typed ownership, never a guessed legacy ID.
var auditResourceSpecs = []auditResourceSpec{
	{"user", models.ResourceUsers, models.ActionRead, "users", "user", []audit.EventKind{audit.SignIn, audit.SignOut, audit.SessionsRevoked, audit.UserSecurityChanged, audit.PermissionChanged, audit.IdentityProvisioned}},
	{"organization_member", models.ResourceUsers, models.ActionRead, "users", "user", nil},
	{"role", models.ResourceRoles, models.ActionRead, "custom_roles", "", []audit.EventKind{audit.PermissionChanged}},
	{"account", models.ResourceAccounts, models.ActionRead, "whatsapp_accounts", "", []audit.EventKind{audit.IntegrationChanged, audit.RegistrationUpdated}},
	{"webhook", models.ResourceWebhooks, models.ActionRead, "webhooks", "", []audit.EventKind{audit.IntegrationChanged}},
	{"custom_action", models.ResourceCustomActions, models.ActionRead, "custom_actions", "", []audit.EventKind{audit.IntegrationChanged}},
	{"sso_provider", models.ResourceSettingsSSO, models.ActionRead, "sso_providers", "", []audit.EventKind{audit.IntegrationChanged}},
	{"channel_connection", models.ResourceSettingsChannels, models.ActionRead, "channel_connections", "", []audit.EventKind{audit.IntegrationChanged, audit.IntegrationValidated}},
	{"inbox_integration", models.ResourceSettingsGeneral, models.ActionWrite, "inbox_integrations", "", []audit.EventKind{audit.IntegrationChanged}},
	{"inbox_integration_settings", models.ResourceSettingsGeneral, models.ActionWrite, "organizations", "organization", []audit.EventKind{audit.IntegrationChanged}},
	{"inbox_sla_escalation_settings", models.ResourceSettingsGeneral, models.ActionRead, "whatsapp_accounts", "", nil},
	{"contact", models.ResourceContacts, models.ActionRead, "contacts", "", nil},
	{"team", models.ResourceTeams, models.ActionRead, "teams", "", nil},
	{"template", models.ResourceTemplates, models.ActionRead, "templates", "", nil},
	{"campaign", models.ResourceCampaigns, models.ActionRead, "bulk_message_campaigns", "", nil},
	{"chatbot_flow", models.ResourceFlowsChatbot, models.ActionRead, "chatbot_flows", "", nil},
	{"keyword_rule", models.ResourceChatbotKeywords, models.ActionRead, "keyword_rules", "", nil},
	{"ai_context", models.ResourceChatbotAI, models.ActionRead, "ai_contexts", "", nil},
	{"canned_response", models.ResourceCannedResponses, models.ActionRead, "canned_responses", "", nil},
	{"ivr_flow", models.ResourceIVRFlows, models.ActionRead, "ivr_flows", "", nil},
	{"organization", models.ResourceOrganizations, models.ActionRead, "organizations", "organization", nil},
	{models.ResourceSettingsNotification, models.ResourceSettingsNotification, models.ActionRead, "users", "self", nil},
	{models.ResourceSettingsChatbot, models.ResourceSettingsChatbot, models.ActionRead, "chatbot_settings", "settings", nil},
	{models.ResourceSettingsChatbotMessages, models.ResourceSettingsChatbot, models.ActionRead, "chatbot_settings", "settings", nil},
	{models.ResourceSettingsChatbotAgents, models.ResourceSettingsChatbot, models.ActionRead, "chatbot_settings", "settings", nil},
	{models.ResourceSettingsChatbotHours, models.ResourceSettingsChatbot, models.ActionRead, "chatbot_settings", "settings", nil},
	{models.ResourceSettingsChatbotSLA, models.ResourceSettingsChatbot, models.ActionRead, "chatbot_settings", "settings", nil},
	{models.ResourceSettingsChatbotAI, models.ResourceSettingsChatbot, models.ActionRead, "chatbot_settings", "settings", nil},
	{"permission", models.ResourceRoles, models.ActionRead, "organizations", "organization", []audit.EventKind{audit.AuthorizationDenied}},
	{"audit_logs", models.ResourceAuditLogs, models.ActionRead, "organizations", "organization", []audit.EventKind{audit.Exported}},
}

func (s auditResourceSpec) allowed(v auditViewer) bool {
	if !v.has(s.Permission, s.Action) {
		return false
	}
	if s.Type == "inbox_sla_escalation_settings" {
		return v.has(models.ResourceContacts, models.ActionRead) && v.has(models.ResourceChat, models.ActionRead)
	}
	return true
}

func (s auditResourceSpec) target(v auditViewer, live bool) (string, []any) {
	suffix := ""
	if live {
		suffix = " AND ar.deleted_at IS NULL"
	}
	switch s.Target {
	case "organization":
		return "EXISTS (SELECT 1 FROM organizations ar WHERE ar.id=audit_logs.resource_id AND ar.id=?" + suffix + ")", []any{v.Org}
	case "user":
		membership := ""
		if live {
			membership = " AND uo.deleted_at IS NULL"
		}
		return "EXISTS (SELECT 1 FROM users ar WHERE ar.id=audit_logs.resource_id AND (ar.organization_id=? OR EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.user_id=ar.id AND uo.organization_id=?" + membership + "))" + suffix + ")", []any{v.Org, v.Org}
	case "self":
		// The viewer's selected membership was already verified. Personal
		// preferences stay personal when that identity uses another organisation.
		return "EXISTS (SELECT 1 FROM users ar WHERE ar.id=audit_logs.resource_id AND ar.id=?" + suffix + ")", []any{v.User}
	case "settings":
		return "(audit_logs.resource_id=? OR EXISTS (SELECT 1 FROM chatbot_settings ar WHERE ar.id=audit_logs.resource_id AND ar.organization_id=?" + suffix + "))", []any{v.Org, v.Org}
	default:
		return "EXISTS (SELECT 1 FROM " + s.Table + " ar WHERE ar.id=audit_logs.resource_id AND ar.organization_id=?" + suffix + ")", []any{v.Org}
	}
}

func auditCaseScope(db *gorm.DB, v auditViewer) *gorm.DB {
	return scopeInboxCaseHistoryWithContactRead(db.Session(&gorm.Session{NewDB: true}).Model(&models.InboxCase{}), v.User, v.Org, v.has(models.ResourceContacts, models.ActionRead)).
		Where("inbox_cases.id=audit_logs.case_id AND inbox_cases.account_id=audit_logs.account_id AND inbox_cases.id=audit_logs.resource_id").Select("1")
}

func scopeAuditLogs(db *gorm.DB, v auditViewer) *gorm.DB {
	allowed := db.Session(&gorm.Session{NewDB: true}).Where("FALSE")
	for _, spec := range auditResourceSpecs {
		if !spec.allowed(v) {
			continue
		}
		exists, args := spec.target(v, false)
		if len(spec.Trusted) > 0 {
			kinds := make([]string, len(spec.Trusted))
			for i, k := range spec.Trusted {
				kinds[i] = string(k)
			}
			exists = "(" + exists + " OR (audit_logs.context_version=1 AND audit_logs.category='security' AND audit_logs.event IN ?))"
			args = append(args, kinds)
		}
		args = append([]any{spec.Type}, args...)
		allowed = allowed.Or("audit_logs.resource_type=? AND ("+exists+")", args...)
	}
	if v.has(models.ResourceChat, models.ActionRead) {
		allowed = allowed.Or("audit_logs.resource_type='inbox_case' AND audit_logs.context_version=1 AND audit_logs.category='security' AND audit_logs.event=? AND EXISTS (?)", string(audit.CaseChanged), auditCaseScope(db, v))
	}
	return db.Model(&models.AuditLog{}).Where("audit_logs.organization_id=?", v.Org).Where(allowed)
}

func auditAvailability(v auditViewer) (string, []any) {
	query := "CASE"
	args := []any{}
	for _, spec := range auditResourceSpecs {
		if !spec.allowed(v) {
			continue
		}
		exists, values := spec.target(v, true)
		query += " WHEN audit_logs.resource_type=? THEN (" + exists + ")"
		args = append(args, spec.Type)
		args = append(args, values...)
	}
	query += " WHEN audit_logs.resource_type='inbox_case' THEN true ELSE false END AS resource_available"
	return query, args
}
