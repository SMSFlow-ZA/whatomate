package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	extsecurity "github.com/shridarpatil/whatomate/pkg/extensions/security"
	"github.com/zerodha/fastglue"
)

// Security is constructed from trusted server configuration, never a user API.
// An unset key retains ordinary upstream authentication but enables no MFA bridge.
func (a *App) initializeExtensionSecurity() error {
	if a.Config == nil {
		return fmt.Errorf("missing extension configuration")
	}
	raw := strings.TrimSpace(a.Config.Extensions.SecurityKey)
	if raw == "" {
		return nil
	}
	if a.DB == nil {
		return fmt.Errorf("extension security database unavailable")
	}
	key, e := base64.StdEncoding.DecodeString(raw)
	if e != nil || len(key) != 32 {
		return fmt.Errorf("extension security key must encode 32 bytes")
	}
	db, e := a.DB.DB()
	if e != nil {
		return e
	}
	service, e := extsecurity.New(db, key, extsecurity.Config{Drift: 1, EnrollmentTTL: 10 * time.Minute, MaxBreakGlass: time.Hour, AllowedBreakGlassPermissions: []string{"cases:read", "cases:work.assigned"}})
	if e != nil {
		return e
	}
	if e = a.extensionSchemaMigration(context.Background(), db, func() error { return service.Migrate(context.Background()) }); e != nil {
		return e
	}
	a.ExtensionSecurity = service
	if seconds := a.Config.Extensions.MFAFreshnessSeconds; seconds > 0 {
		a.ExtensionMFAFreshness = time.Duration(seconds) * time.Second
	}
	return nil
}

// Routes retain ordinary authentication/CSRF middleware. Enrollment and challenge
// use a primary-authenticated session; case APIs independently require MFA.
func (a *App) RegisterExtensionSecurityRoutes(g *fastglue.Fastglue) {
	g.GET("/api/extensions/security/mfa", a.ExtensionMFAStatus)
	g.POST("/api/extensions/security/mfa/enroll", a.ExtensionMFABegin)
	g.POST("/api/extensions/security/mfa/confirm", a.ExtensionMFAConfirm)
	g.POST("/api/extensions/security/mfa/challenge", a.ExtensionMFAChallenge)
	g.POST("/api/extensions/security/mfa/revoke", a.ExtensionMFARevoke)
	g.POST("/api/extensions/security/emergency/request", a.ExtensionEmergencyRequest)
	g.POST("/api/extensions/security/emergency/approve", a.ExtensionEmergencyApprove)
	g.POST("/api/extensions/security/emergency/use", a.ExtensionEmergencyUse)
	g.POST("/api/extensions/security/emergency/revoke", a.ExtensionEmergencyRevoke)
}
