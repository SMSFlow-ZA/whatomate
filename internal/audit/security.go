package audit

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EventKind string

const (
	SignIn               EventKind = "auth.sign_in"
	SignOut              EventKind = "auth.sign_out"
	SessionsRevoked      EventKind = "auth.sessions_revoked"
	UserSecurityChanged  EventKind = "user.security_changed"
	IdentityProvisioned  EventKind = "identity.provisioned"
	PermissionChanged    EventKind = "permissions.changed"
	IntegrationChanged   EventKind = "integration.changed"
	IntegrationValidated EventKind = "integration.validated"
	RegistrationUpdated  EventKind = "integration.registration_updated"
	AuthorizationDenied  EventKind = "authorization.denied"
	Exported             EventKind = "audit.exported"
	CaseChanged          EventKind = "inbox.case_changed"
)

type Outcome string

const (
	Success Outcome = "success"
	Denied  Outcome = "denied"
	Failed  Outcome = "failed"
)

type Reason string

const (
	Password                  Reason = "password"
	MicrosoftSSO              Reason = "microsoft_sso"
	FederatedSSO              Reason = "federated_sso"
	SessionCreationFailed     Reason = "session_creation_failed"
	InvalidCredentials        Reason = "invalid_credentials"
	DisabledAccount           Reason = "disabled_account"
	SessionStorageUnavailable Reason = "session_storage_unavailable"
	SignedOut                 Reason = "signed_out"
	AlreadySignedOut          Reason = "already_signed_out"
	PasswordChanged           Reason = "password_changed"
	PasswordReset             Reason = "password_reset"
	UserDisabled              Reason = "user_disabled"
	UserDeleted               Reason = "user_deleted"
	MembershipChanged         Reason = "membership_changed"
	RoleChanged               Reason = "role_changed"
	RoleCreated               Reason = "role_created"
	RoleDeleted               Reason = "role_deleted"
	UserRoleChanged           Reason = "user_role_changed"
	ManualRevocation          Reason = "manual_revocation"
	BeforeSecurityUpdate      Reason = "before_security_update"
	SecuritySettingsChanged   Reason = "security_settings_changed"
	SSOIdentityChanged        Reason = "sso_identity_changed"
	UserEnabled               Reason = "user_enabled"
	MembershipAdded           Reason = "membership_added"
	MembershipRemoved         Reason = "membership_removed"
	UserAuthorityChanged      Reason = "user_authority_changed"
	Created                   Reason = "created"
	RegistrationCreated       Reason = "registration_created"
	RegistrationJoined        Reason = "registration_joined"
	SSOProvisioned            Reason = "sso_provisioned"
	RegistrationActive        Reason = "registration_active"
	RegistrationPending       Reason = "registration_pending"
	RegistrationInactive      Reason = "registration_inactive"
	ConfigurationChanged      Reason = "configuration_changed"
	CredentialRotated         Reason = "credential_rotated"
	Validated                 Reason = "validated"
	Disabled                  Reason = "disabled"
	Deleted                   Reason = "deleted"
	PermissionMissing         Reason = "permission_missing"
	ProtectedResource         Reason = "protected_resource"
	ExportCompleted           Reason = "export_completed"
	ExportPrepared            Reason = "export_prepared"
	StatusChanged             Reason = "status_changed"
	PriorityChanged           Reason = "priority_changed"
	AssignmentChanged         Reason = "assignment_changed"
	FieldChanged              Reason = "field_changed"
	WorkflowAction            Reason = "workflow_action"
)

var ErrInvalidEvent = errors.New("invalid security audit event")
var ErrConflictingEvent = errors.New("conflicting security audit event")

var eventReasons = map[EventKind][]Reason{
	SignIn:               {Password, MicrosoftSSO, FederatedSSO, InvalidCredentials, DisabledAccount, SessionStorageUnavailable, SessionCreationFailed},
	SignOut:              {SignedOut, AlreadySignedOut, SessionStorageUnavailable},
	SessionsRevoked:      {PasswordChanged, PasswordReset, UserDisabled, UserDeleted, MembershipChanged, MembershipRemoved, RoleChanged, ManualRevocation, BeforeSecurityUpdate, SessionStorageUnavailable},
	UserSecurityChanged:  {PasswordChanged, PasswordReset, UserDisabled, UserEnabled, SSOIdentityChanged, SecuritySettingsChanged},
	IdentityProvisioned:  {RegistrationCreated, RegistrationJoined, SSOProvisioned},
	PermissionChanged:    {RoleCreated, RoleChanged, RoleDeleted, MembershipChanged, MembershipAdded, MembershipRemoved, UserRoleChanged, UserAuthorityChanged},
	IntegrationChanged:   {Created, ConfigurationChanged, CredentialRotated, Disabled, Deleted},
	IntegrationValidated: {Validated},
	RegistrationUpdated:  {RegistrationActive, RegistrationPending, RegistrationInactive},
	AuthorizationDenied:  {PermissionMissing, ProtectedResource},
	Exported:             {ExportCompleted, ExportPrepared},
	CaseChanged:          {StatusChanged, PriorityChanged, AssignmentChanged, FieldChanged, WorkflowAction},
}
var eventPermissions = func() map[string]bool {
	result := map[string]bool{}
	for _, p := range models.DefaultPermissions() {
		result[p.Resource+":"+p.Action] = true
	}
	return result
}()

// RequestContext contains server-owned provenance. Never populate RequestID
// from an incoming header. Only ResolveOrigin may qualify forwarded addresses.
type RequestContext struct {
	RequestID uuid.UUID
	Origin    Origin
	AccountID *uuid.UUID
	CaseID    *uuid.UUID
}

// SecurityEvent has no payload/changes/request/session/token field. Callers
// supply verified IDs and fixed codes, not serialized handler inputs or errors.
type SecurityEvent struct {
	OrganizationID uuid.UUID
	ActorID        uuid.UUID
	ResourceID     uuid.UUID
	ResourceType   string
	Kind           EventKind
	Outcome        Outcome
	Reason         Reason
	Permission     string
	Context        RequestContext
}

func eventAllowed(e SecurityEvent) bool {
	if e.OrganizationID == uuid.Nil || e.ResourceID == uuid.Nil || e.Context.RequestID == uuid.Nil {
		return false
	}
	if e.Outcome != Success && e.Outcome != Denied && e.Outcome != Failed {
		return false
	}
	if e.ActorID == uuid.Nil && e.Kind != IdentityProvisioned && (e.Kind != SignIn || e.Outcome == Success) {
		return false
	}
	if e.Kind == IdentityProvisioned && (e.ActorID != uuid.Nil || e.ResourceType != "user" || e.Outcome != Success) {
		return false
	}
	allowed := false
	for _, reason := range eventReasons[e.Kind] {
		allowed = allowed || e.Reason == reason
	}
	if !allowed {
		return false
	}
	if e.Kind == SignIn {
		if e.Outcome == Success && e.Reason != Password && e.Reason != MicrosoftSSO && e.Reason != FederatedSSO {
			return false
		}
		if e.Outcome == Denied && (e.ActorID != uuid.Nil || (e.Reason != InvalidCredentials && e.Reason != DisabledAccount)) {
			return false
		}
		if e.Outcome == Failed && e.Reason != SessionStorageUnavailable && e.Reason != SessionCreationFailed {
			return false
		}
	}
	if e.Kind == SignOut && (e.Outcome == Denied || (e.Outcome == Failed) != (e.Reason == SessionStorageUnavailable)) {
		return false
	}
	if e.Kind == SessionsRevoked && ((e.Outcome == Failed) != (e.Reason == SessionStorageUnavailable)) {
		return false
	}
	if e.Kind == UserSecurityChanged && (e.ResourceType != "user" || e.Outcome != Success) {
		return false
	}
	if e.Kind == Exported && e.Outcome != Success {
		return false
	}
	if e.Kind != SignIn && e.Kind != AuthorizationDenied && e.Outcome == Denied {
		return false
	}
	if e.Permission != "" {
		if !eventPermissions[e.Permission] {
			return false
		}
	}
	if e.Kind == AuthorizationDenied && (e.Outcome != Denied || e.Permission == "" || e.ResourceType != "permission" || e.ResourceID != e.OrganizationID) {
		return false
	}
	if e.Kind != AuthorizationDenied && e.Permission != "" {
		return false
	}
	if e.Kind == SignIn || e.Kind == SignOut || e.Kind == SessionsRevoked {
		if e.ResourceType != "user" || ((e.Kind == SignIn || e.Kind == SignOut) && e.ActorID != uuid.Nil && e.ActorID != e.ResourceID) {
			return false
		}
	}
	if e.Kind == PermissionChanged {
		switch e.Reason {
		case RoleCreated, RoleChanged, RoleDeleted:
			if e.ResourceType != "role" {
				return false
			}
		case MembershipChanged, MembershipAdded, MembershipRemoved, UserRoleChanged, UserAuthorityChanged:
			if e.ResourceType != "user" {
				return false
			}
		}
	}
	if e.Kind == IntegrationChanged || e.Kind == IntegrationValidated {
		switch e.ResourceType {
		case "account", "webhook", "custom_action", "channel_connection", "inbox_integration", "inbox_integration_settings", "sso_provider":
		default:
			return false
		}
		if e.Outcome != Success || e.ResourceType == "inbox_integration_settings" && e.ResourceID != e.OrganizationID {
			return false
		}
		if e.Kind == IntegrationValidated && e.ResourceType != "channel_connection" {
			return false
		}
	}
	if e.Kind == RegistrationUpdated && (e.ResourceType != "account" || e.Outcome != Success) {
		return false
	}
	if e.Kind == Exported && (e.ResourceType != "audit_logs" || e.ResourceID != e.OrganizationID) {
		return false
	}
	if e.Kind == CaseChanged {
		if e.ResourceType != "inbox_case" || e.Context.CaseID == nil || *e.Context.CaseID != e.ResourceID || e.Context.AccountID == nil {
			return false
		}
	} else if e.Context.CaseID != nil {
		return false
	}
	if e.Context.AccountID != nil && e.Kind != CaseChanged && ((e.Kind != IntegrationChanged && e.Kind != RegistrationUpdated) || e.ResourceType != "account" || *e.Context.AccountID != e.ResourceID) {
		return false
	}
	if e.Context.Origin.Source == "unknown" {
		return e.Context.Origin.IP == ""
	}
	if e.Context.Origin.Source != "peer" && e.Context.Origin.Source != "forwarded" {
		return false
	}
	ip, err := netip.ParseAddr(e.Context.Origin.IP)
	return err == nil && !ip.IsUnspecified() && !ip.IsMulticast() && ip.Zone() == ""
}

// RecordSecurity writes synchronously with a bounded deadline, in the caller's
// transaction when present. The partial unique constraint makes one event per
// request/kind/resource durable across concurrent/restarted callers. A missing
// constraint fails instead of silently disabling that guarantee.
func RecordSecurity(db *gorm.DB, e SecurityEvent) (captureErr error) {
	if db == nil || !eventAllowed(e) {
		return ErrInvalidEvent
	}
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Drivers may return an opaque connection error when cancelling a blocked
	// statement. Preserve our deadline/cancellation category without logging the
	// driver's SQL or parameters. Run before the deferred cancel above.
	defer func() {
		if captureErr != nil && ctx.Err() != nil {
			captureErr = ctx.Err()
		}
	}()
	db = db.WithContext(ctx)
	var org models.Organization
	if err := db.Select("id").First(&org, "id=?", e.OrganizationID).Error; err != nil {
		return err
	}
	name := "Unauthenticated"
	if e.Kind == IdentityProvisioned {
		name = "Provisioning policy"
	}
	if e.ActorID != uuid.Nil {
		var actor models.User
		if err := db.Select("users.id,users.full_name").Where("id=? AND is_active AND (is_super_admin OR organization_id=? OR EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.user_id=users.id AND uo.organization_id=? AND uo.deleted_at IS NULL))", e.ActorID, e.OrganizationID, e.OrganizationID).First(&actor).Error; err != nil {
			return err
		}
		name = actor.FullName
	}
	var target int64
	switch e.ResourceType {
	case "user":
		if e.Kind == IdentityProvisioned {
			// A policy-created membership is not an authenticated administrator.
			// Verify the committed target, active membership and scoped role instead.
			if err := db.Model(&models.User{}).Where("id=? AND is_active AND EXISTS (SELECT 1 FROM user_organizations uo JOIN custom_roles r ON r.id=uo.role_id AND r.organization_id=uo.organization_id AND r.deleted_at IS NULL WHERE uo.user_id=users.id AND uo.organization_id=? AND uo.deleted_at IS NULL)", e.ResourceID, e.OrganizationID).Count(&target).Error; err != nil {
				return err
			}
			break
		}
		if err := db.Unscoped().Model(&models.User{}).Where("id=? AND (organization_id=? OR EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.user_id=users.id AND uo.organization_id=?) OR (is_super_admin AND id=?))", e.ResourceID, e.OrganizationID, e.OrganizationID, e.ActorID).Count(&target).Error; err != nil {
			return err
		}
	case "role":
		if err := db.Unscoped().Model(&models.CustomRole{}).Where("id=? AND organization_id=?", e.ResourceID, e.OrganizationID).Count(&target).Error; err != nil {
			return err
		}
	case "permission", "audit_logs", "inbox_integration_settings":
		target = 1
	case "inbox_case":
		if err := db.Model(&models.InboxCase{}).Where("id=? AND organization_id=? AND account_id=?", e.ResourceID, e.OrganizationID, e.Context.AccountID).Count(&target).Error; err != nil {
			return err
		}
	default:
		table := map[string]string{"account": "whatsapp_accounts", "webhook": "webhooks", "custom_action": "custom_actions", "channel_connection": "channel_connections", "inbox_integration": "inbox_integrations", "sso_provider": "sso_providers"}[e.ResourceType]
		if table == "" {
			return ErrInvalidEvent
		}
		if err := db.Table(table).Where("id=? AND organization_id=?", e.ResourceID, e.OrganizationID).Count(&target).Error; err != nil {
			return err
		}
	}
	if target != 1 {
		return ErrInvalidEvent
	}
	var account, caseID *uuid.UUID
	if e.Context.AccountID != nil {
		id := *e.Context.AccountID
		var count int64
		if err := db.Unscoped().Model(&models.WhatsAppAccount{}).Where("id=? AND organization_id=?", id, e.OrganizationID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrInvalidEvent
		}
		account = &id
	}
	if e.Context.CaseID != nil {
		id := *e.Context.CaseID
		caseID = &id
	}
	origin := e.Context.Origin
	if origin.IP != "" {
		ip, _ := netip.ParseAddr(origin.IP)
		origin.IP = ip.Unmap().String()
	}
	entry := models.AuditLog{OrganizationID: e.OrganizationID, ResourceType: e.ResourceType, ResourceID: e.ResourceID, UserID: e.ActorID, UserName: name, Action: models.AuditActionEvent, Changes: models.JSONBArray{}, AuditContext: models.AuditContext{ContextVersion: 1, Category: "security", Event: string(e.Kind), Outcome: string(e.Outcome), Reason: string(e.Reason), Permission: e.Permission, RequestID: e.Context.RequestID.String(), OriginIP: origin.IP, OriginSource: origin.Source, ActorVerified: e.ActorID != uuid.Nil, AccountID: account, CaseID: caseID}}
	result := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "organization_id"}, {Name: "request_id"}, {Name: "event"}, {Name: "resource_type"}, {Name: "resource_id"}}, TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "context_version = 1 AND request_id <> '' AND event <> ''"}}}, DoNothing: true}).Create(&entry)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var previous models.AuditLog
	if err := db.Where("organization_id=? AND request_id=? AND event=? AND resource_type=? AND resource_id=? AND context_version=1", entry.OrganizationID, entry.RequestID, entry.Event, entry.ResourceType, entry.ResourceID).First(&previous).Error; err != nil {
		return err
	}
	sameID := func(a, b *uuid.UUID) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
	if previous.UserID != entry.UserID || previous.Outcome != entry.Outcome || previous.Reason != entry.Reason || previous.Permission != entry.Permission || previous.OriginIP != entry.OriginIP || previous.OriginSource != entry.OriginSource || previous.ActorVerified != entry.ActorVerified || !sameID(previous.AccountID, entry.AccountID) || !sameID(previous.CaseID, entry.CaseID) {
		return ErrConflictingEvent
	}
	return nil
}

// ErrorCode is suitable for operational logs. It never includes SQL, identity,
// payload, endpoint, credentials or an arbitrary provider error message.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrInvalidEvent) {
		return "invalid_event"
	}
	if errors.Is(err, ErrConflictingEvent) {
		return "conflicting_event"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		if len(pgerr.Code) != 5 {
			return "database_error"
		}
		for _, c := range pgerr.Code {
			if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
				return "database_error"
			}
		}
		return "database_" + pgerr.Code
	}
	return "storage_unavailable"
}
