package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"github.com/zerodha/logf"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func auditUserRequest(t *testing.T, f inboxAuditFixture, target uuid.UUID, body any) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, body)
	testutil.SetFullAuthContext(r, f.org.ID, f.user.ID, f.user.RoleID, f.user.IsSuperAdmin)
	if target != uuid.Nil {
		testutil.SetPathParam(r, "id", target.String())
		testutil.SetPathParam(r, "member_id", target.String())
	}
	return r
}

func failOwnedSecurityKind(t *testing.T, f inboxAuditFixture, kind audit.EventKind) {
	t.Helper()
	name := "audit_user_fault_" + uuid.NewString()
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.OrganizationID == f.org.ID && row.Event == string(kind) {
			_ = db.AddError(errors.New("synthetic storage failure"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.db.Callback().Create().Remove(name)) })
}

func TestInboxAuditUserPasswordAndRoleCommitWithIndependentFenceEvidence(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	role := testutil.CreateAgentRole(t, f.db, f.org.ID)
	target := testutil.CreateTestUser(t, f.db, f.org.ID)
	oldStamp := target.AuthStamp
	r := auditUserRequest(t, f, target.ID, UserRequest{Password: "Synthetic reset credential 123!", RoleID: &role.ID})
	require.NoError(t, f.app.UpdateUser(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	var current models.User
	require.NoError(t, f.db.First(&current, "id=?", target.ID).Error)
	require.NotEqual(t, oldStamp, current.AuthStamp)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(current.PasswordHash), []byte("Synthetic reset credential 123!")))
	var membership models.UserOrganization
	require.NoError(t, f.db.First(&membership, "user_id=? AND organization_id=?", target.ID, f.org.ID).Error)
	require.Equal(t, role.ID, *membership.RoleID)
	rows := f.securityRows(t)
	require.Len(t, rows, 3)
	seen := map[string]string{}
	for _, row := range rows {
		seen[row.Event] = row.Reason
		require.Equal(t, rows[0].RequestID, row.RequestID)
		require.Equal(t, f.user.ID, row.UserID)
		require.Empty(t, row.Changes)
	}
	require.Equal(t, string(audit.BeforeSecurityUpdate), seen[string(audit.SessionsRevoked)])
	require.Equal(t, string(audit.PasswordReset), seen[string(audit.UserSecurityChanged)])
	require.Equal(t, string(audit.UserRoleChanged), seen[string(audit.PermissionChanged)])
	_, err := middleware.CurrentIdentity(f.db, &middleware.JWTClaims{UserID: target.ID, OrganizationID: f.org.ID, SessionID: "synthetic-old-session", AuthStamp: oldStamp})
	require.ErrorIs(t, err, middleware.ErrSessionRevoked)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "Synthetic reset credential")
	require.NotContains(t, string(encoded), current.PasswordHash)
}

func TestInboxAuditUserCaptureFailureRollsBackPasswordRoleAndMembershipButRetainsFence(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	role := testutil.CreateAgentRole(t, f.db, f.org.ID)
	target := testutil.CreateTestUser(t, f.db, f.org.ID)
	oldHash, oldStamp := target.PasswordHash, target.AuthStamp
	failOwnedSecurityKind(t, f, audit.UserSecurityChanged)
	r := auditUserRequest(t, f, target.ID, UserRequest{Password: "Synthetic rejected credential 123!", RoleID: &role.ID})
	require.NoError(t, f.app.UpdateUser(r))
	require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
	var retained models.User
	require.NoError(t, f.db.First(&retained, "id=?", target.ID).Error)
	require.Equal(t, oldHash, retained.PasswordHash)
	require.Equal(t, oldStamp, retained.AuthStamp)
	require.True(t, sameAuditRole(target.RoleID, retained.RoleID))
	var membership models.UserOrganization
	require.NoError(t, f.db.First(&membership, "user_id=? AND organization_id=?", target.ID, f.org.ID).Error)
	require.True(t, sameAuditRole(target.RoleID, membership.RoleID))
	version, err := middleware.SessionVersion(context.Background(), f.app.Redis, target.ID)
	require.NoError(t, err)
	require.NotEmpty(t, version, "the real pre-update Redis fence is not rolled back")
	rows := f.securityRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, string(audit.SessionsRevoked), rows[0].Event)
	require.Equal(t, string(audit.BeforeSecurityUpdate), rows[0].Reason)
}

type auditSecondFenceFailure struct {
	key   string
	calls atomic.Int32
}

func (h *auditSecondFenceFailure) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *auditSecondFenceFailure) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *auditSecondFenceFailure) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "set" && len(cmd.Args()) > 1 && cmd.Args()[1] == h.key && h.calls.Add(1) == 2 {
			return errors.New("synthetic final fence unavailable")
		}
		return next(ctx, cmd)
	}
}

func TestInboxAuditPasswordCommitRemainsRecordedWhenFinalRedisFenceFails(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	options := *f.app.Redis.Options()
	client := redis.NewClient(&options)
	t.Cleanup(func() { _ = client.Close() })
	hook := &auditSecondFenceFailure{key: middleware.VersionKey(f.user.ID)}
	client.AddHook(hook)
	f.app.Redis = client
	oldStamp := f.user.AuthStamp
	r := auditUserRequest(t, f, uuid.Nil, ChangePasswordRequest{CurrentPassword: auditTestPassword, NewPassword: "Synthetic new self credential 123!"})
	require.NoError(t, f.app.ChangePassword(r))
	require.Equal(t, 503, r.RequestCtx.Response.StatusCode())
	require.EqualValues(t, 2, hook.calls.Load())
	var current models.User
	require.NoError(t, f.db.First(&current, "id=?", f.user.ID).Error)
	require.NotEqual(t, oldStamp, current.AuthStamp)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(current.PasswordHash), []byte("Synthetic new self credential 123!")))
	rows := f.securityRows(t)
	require.Len(t, rows, 2)
	require.Equal(t, string(audit.SessionsRevoked), rows[0].Event)
	require.Equal(t, string(audit.UserSecurityChanged), rows[1].Event)
	require.Equal(t, string(audit.PasswordChanged), rows[1].Reason)
	require.Equal(t, string(audit.Success), rows[1].Outcome, "the DB change committed despite HTTP503")
}

func TestInboxAuditMembershipRemovalIsSelectedOrgOnlyAndAtomicAcrossBothAPIs(t *testing.T) {
	for _, api := range []string{"users", "organizations"} {
		for _, fail := range []bool{false, true} {
			t.Run(api+map[bool]string{false: "/commit", true: "/rollback"}[fail], func(t *testing.T) {
				f := newInboxAuditAuthFixture(t)
				home := testutil.CreateTestOrganization(t, f.db)
				target := testutil.CreateTestUser(t, f.db, home.ID)
				role := testutil.CreateAgentRole(t, f.db, f.org.ID)
				membership := models.UserOrganization{UserID: target.ID, OrganizationID: f.org.ID, RoleID: &role.ID}
				require.NoError(t, f.db.Create(&membership).Error)
				if fail {
					failOwnedSecurityKind(t, f, audit.SessionsRevoked)
				}
				r := auditUserRequest(t, f, target.ID, nil)
				if api == "users" {
					require.NoError(t, f.app.DeleteUser(r))
				} else {
					require.NoError(t, f.app.RemoveOrganizationMember(r))
				}
				if fail {
					require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
				} else {
					require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
				}
				_, err := middleware.CurrentIdentity(f.db, &middleware.JWTClaims{UserID: target.ID, OrganizationID: home.ID})
				require.NoError(t, err, "unrelated home organization remains accessible")
				_, err = middleware.CurrentIdentity(f.db, &middleware.JWTClaims{UserID: target.ID, OrganizationID: f.org.ID})
				rows := f.securityRows(t)
				if fail {
					require.NoError(t, err)
					require.Empty(t, rows)
				} else {
					require.Error(t, err)
					require.Len(t, rows, 2)
					for _, row := range rows {
						require.Equal(t, string(audit.MembershipRemoved), row.Reason)
						require.Equal(t, f.org.ID, row.OrganizationID)
						require.Equal(t, target.ID, row.ResourceID)
					}
				}
				var other int64
				require.NoError(t, f.db.Model(&models.AuditLog{}).Where("organization_id=? AND context_version=1", home.ID).Count(&other).Error)
				require.Zero(t, other)
			})
		}
	}
}

func TestInboxAuditUserDeletionRollsBackAllMembershipsOnCaptureFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			f := newInboxAuditAuthFixture(t)
			target := testutil.CreateTestUser(t, f.db, f.org.ID)
			if fail {
				failOwnedSecurityKind(t, f, audit.SessionsRevoked)
			}
			r := auditUserRequest(t, f, target.ID, nil)
			require.NoError(t, f.app.DeleteUser(r))
			var retained models.User
			err := f.db.First(&retained, "id=?", target.ID).Error
			rows := f.securityRows(t)
			if fail {
				require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
				require.NoError(t, err)
				require.Empty(t, rows)
			} else {
				require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
				require.ErrorIs(t, err, gorm.ErrRecordNotFound)
				require.Len(t, rows, 1)
				require.Equal(t, string(audit.UserDeleted), rows[0].Reason)
			}
			var memberships int64
			require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=?", target.ID).Count(&memberships).Error)
			if fail {
				require.EqualValues(t, 1, memberships)
			} else {
				require.Zero(t, memberships)
			}
		})
	}
}

func TestInboxAuditMalformedUserBodyAndUnavailableIdentityNeverLogSecrets(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	var logs auditLockedLog
	f.app.Log = logf.New(logf.Opts{Writer: &logs, Level: logf.ErrorLevel})
	r := auditUserRequest(t, f, f.user.ID, nil)
	r.RequestCtx.Request.SetBodyString(`{"password":"synthetic-private-malformed-password",`)
	require.NoError(t, f.app.UpdateUser(r))
	require.Equal(t, 400, r.RequestCtx.Response.StatusCode())
	require.NotContains(t, logs.text(), "synthetic-private-malformed-password")
	require.Contains(t, logs.text(), "invalid_request")
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	f.app.DB = nil
	r.RequestCtx.SetUserValue(middleware.ContextKeyClaims, &middleware.JWTClaims{UserID: f.user.ID, OrganizationID: f.org.ID})
	f.app.observeDenial(r, f.org.ID, f.user.ID, models.ResourceUsers, models.ActionWrite)
	require.Contains(t, logs.text(), "storage_unavailable")
	require.Empty(t, f.securityRows(t))
}

func TestInboxAuditUserCreationAndRestorationAreAtomicWithMembership(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(map[bool]string{false: "create", true: "restore"}[restore]+map[bool]string{false: "/commit", true: "/rollback"}[fail], func(t *testing.T) {
				f := newInboxAuditAuthFixture(t)
				role := testutil.CreateAgentRole(t, f.db, f.org.ID)
				email := uuid.NewString() + "@example.invalid"
				var previous *models.User
				if restore {
					previous = testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithEmail(email))
					require.NoError(t, f.db.Delete(previous).Error)
					require.NoError(t, f.db.Where("user_id=?", previous.ID).Delete(&models.UserOrganization{}).Error)
				}
				if fail {
					failOwnedSecurityKind(t, f, audit.PermissionChanged)
				}
				r := auditUserRequest(t, f, uuid.Nil, UserRequest{Email: email, FullName: "Synthetic new operator", Password: "Synthetic creation password 123!", RoleID: &role.ID})
				require.NoError(t, f.app.CreateUser(r))
				var user models.User
				err := f.db.First(&user, "email=?", email).Error
				rows := f.securityRows(t)
				if fail {
					require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
					require.ErrorIs(t, err, gorm.ErrRecordNotFound)
					require.Empty(t, rows)
				} else {
					require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
					require.NoError(t, err)
					var membership models.UserOrganization
					require.NoError(t, f.db.First(&membership, "user_id=? AND organization_id=?", user.ID, f.org.ID).Error)
					require.Equal(t, role.ID, *membership.RoleID)
					if restore {
						require.Equal(t, previous.ID, user.ID)
						require.Len(t, rows, 2)
					} else {
						require.Len(t, rows, 1)
					}
					require.Equal(t, string(audit.MembershipAdded), rows[0].Reason)
				}
			})
		}
	}
}

func TestInboxAuditMemberAdditionAndRoleChangesRemainScopedAndRollbackTogether(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			f := newInboxAuditAuthFixture(t)
			home := testutil.CreateTestOrganization(t, f.db)
			target := testutil.CreateTestUser(t, f.db, home.ID)
			role := testutil.CreateAgentRole(t, f.db, f.org.ID)
			next := testutil.CreateTestRole(t, f.db, f.org.ID, "Synthetic next role", nil)
			if fail {
				failOwnedSecurityKind(t, f, audit.PermissionChanged)
			}
			add := auditUserRequest(t, f, target.ID, AddMemberRequest{UserID: target.ID, RoleID: &role.ID})
			require.NoError(t, f.app.AddOrganizationMember(add))
			if fail {
				require.Equal(t, 500, add.RequestCtx.Response.StatusCode())
				require.Empty(t, f.securityRows(t))
				return
			}
			require.Equal(t, 200, add.RequestCtx.Response.StatusCode())
			change := auditUserRequest(t, f, target.ID, UpdateMemberRoleRequest{RoleID: next.ID})
			require.NoError(t, f.app.UpdateOrganizationMemberRole(change))
			require.Equal(t, 200, change.RequestCtx.Response.StatusCode())
			unchanged := auditUserRequest(t, f, target.ID, UserRequest{RoleID: &next.ID})
			require.NoError(t, f.app.UpdateUser(unchanged))
			require.Equal(t, 200, unchanged.RequestCtx.Response.StatusCode())
			rows := f.securityRows(t)
			require.Len(t, rows, 2, "a no-op membership role update does not invent an authority change")
			require.Equal(t, string(audit.MembershipAdded), rows[0].Reason)
			require.Equal(t, string(audit.UserRoleChanged), rows[1].Reason)
			var original models.User
			require.NoError(t, f.db.First(&original, "id=?", target.ID).Error)
			require.Equal(t, home.ID, original.OrganizationID)
			require.True(t, sameAuditRole(target.RoleID, original.RoleID))
		})
	}
}

func TestInboxAuditUserSecurityTypeRejectsUnprovenSuccessOrWrongScope(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	for _, mutate := range []func(*audit.SecurityEvent){
		func(e *audit.SecurityEvent) { e.Outcome = audit.Failed },
		func(e *audit.SecurityEvent) { e.ResourceType = "role" },
		func(e *audit.SecurityEvent) { e.Reason = audit.SignedOut },
		func(e *audit.SecurityEvent) { e.ActorID = uuid.Nil },
		func(e *audit.SecurityEvent) { id := uuid.New(); e.Context.AccountID = &id },
	} {
		e := f.event()
		e.Kind = audit.UserSecurityChanged
		e.Reason = audit.PasswordChanged
		mutate(&e)
		require.ErrorIs(t, audit.RecordSecurity(f.db, e), audit.ErrInvalidEvent)
	}
	require.Empty(t, f.securityRows(t))
}

func TestInboxAuditArchivedIdentityCannotBeClaimedFromAnotherOrganization(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	foreign := testutil.CreateTestOrganization(t, f.db)
	target := testutil.CreateTestUser(t, f.db, foreign.ID)
	oldHash := target.PasswordHash
	require.NoError(t, f.db.Delete(target).Error)
	r := auditUserRequest(t, f, uuid.Nil, UserRequest{Email: target.Email, FullName: "Rejected takeover", Password: "Synthetic rejected takeover 123!"})
	require.NoError(t, f.app.CreateUser(r))
	require.Equal(t, 409, r.RequestCtx.Response.StatusCode())
	var retained models.User
	require.NoError(t, f.db.Unscoped().First(&retained, "id=?", target.ID).Error)
	require.Equal(t, foreign.ID, retained.OrganizationID)
	require.Equal(t, oldHash, retained.PasswordHash)
	require.True(t, retained.DeletedAt.Valid)
	require.Empty(t, f.securityRows(t))
}

func TestInboxAuditRestorationRaceCannotOverwriteCommittedCredentials(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	target := testutil.CreateTestUser(t, f.db, f.org.ID)
	require.NoError(t, f.db.Delete(target).Error)
	require.NoError(t, f.db.Where("user_id=?", target.ID).Delete(&models.UserOrganization{}).Error)
	seen := false
	name := "audit_restore_race_" + uuid.NewString()
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		model, ok := tx.Statement.Model.(*models.User)
		if !ok || model.ID != target.ID || seen {
			return
		}
		seen = true
		winner := auditUserRequest(t, f, uuid.Nil, UserRequest{Email: target.Email, FullName: "Winning restore", Password: "Synthetic winning password 123!"})
		require.NoError(t, f.app.CreateUser(winner))
		require.Equal(t, 200, winner.RequestCtx.Response.StatusCode())
	}))
	t.Cleanup(func() { require.NoError(t, f.db.Callback().Update().Remove(name)) })
	loser := auditUserRequest(t, f, uuid.Nil, UserRequest{Email: target.Email, FullName: "Losing restore", Password: "Synthetic losing password 123!"})
	require.NoError(t, f.app.CreateUser(loser))
	require.True(t, seen)
	require.Equal(t, 409, loser.RequestCtx.Response.StatusCode())
	var current models.User
	require.NoError(t, f.db.First(&current, "id=?", target.ID).Error)
	require.Equal(t, "Winning restore", current.FullName)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(current.PasswordHash), []byte("Synthetic winning password 123!")))
	require.Len(t, f.securityRows(t), 2)
}

func TestInboxAuditUserStateAndSSOChangesInvalidateOldIdentityWithAccurateReasons(t *testing.T) {
	for _, mode := range []string{"disable", "enable", "sso", "combined"} {
		t.Run(mode, func(t *testing.T) {
			f := newInboxAuditAuthFixture(t)
			target := testutil.CreateTestUser(t, f.db, f.org.ID)
			if mode == "enable" {
				require.NoError(t, f.db.Model(target).Update("is_active", false).Error)
			}
			require.NoError(t, f.db.Model(target).Updates(map[string]any{"sso_provider": "custom", "sso_provider_id": "synthetic-identity", "sso_binding": "synthetic-binding"}).Error)
			oldStamp := target.AuthStamp
			request := UserRequest{}
			active := mode == "enable"
			id := uuid.Nil
			expected := audit.SSOIdentityChanged
			if mode == "disable" || mode == "enable" || mode == "combined" {
				request.IsActive = &active
				expected = audit.UserDisabled
				if active {
					expected = audit.UserEnabled
				}
			}
			if mode == "sso" || mode == "combined" {
				request.SSOConfigID = &id
				if mode == "combined" {
					expected = audit.SecuritySettingsChanged
				}
			}
			r := auditUserRequest(t, f, target.ID, request)
			require.NoError(t, f.app.UpdateUser(r))
			require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
			var current models.User
			require.NoError(t, f.db.First(&current, "id=?", target.ID).Error)
			require.NotEqual(t, oldStamp, current.AuthStamp)
			if request.IsActive != nil {
				require.Equal(t, *request.IsActive, current.IsActive)
			}
			if request.SSOConfigID != nil {
				require.Empty(t, current.SSOProvider)
				require.Empty(t, current.SSOBinding)
			}
			rows := f.securityRows(t)
			require.Len(t, rows, 2)
			require.Equal(t, string(audit.UserSecurityChanged), rows[1].Event)
			require.Equal(t, string(expected), rows[1].Reason)
		})
	}
}
