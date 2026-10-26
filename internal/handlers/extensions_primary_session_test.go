package handlers

import (
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/middleware"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"testing"
	"time"
)

func TestPrimarySessionDemoMatchesOnlyExactOrganization(t *testing.T) {
	org := uuid.MustParse("3d89880a-f45e-4d40-a166-2e6babbc4240")
	a := &App{Config: &config.Config{}}
	if a.extensionPrimarySessionDemoAllowed(org) {
		t.Fatal("default enabled")
	}
	a.Config.Extensions.PrimarySessionDemoOrganizationID = org.String()
	if !a.extensionPrimarySessionDemoAllowed(org) || a.extensionPrimarySessionDemoAllowed(uuid.New()) || a.extensionPrimarySessionDemoAllowed(uuid.Nil) {
		t.Fatal("organization scope changed")
	}
	a.Config.Extensions.PrimarySessionDemoOrganizationID = " " + org.String()
	if a.extensionPrimarySessionDemoAllowed(org) {
		t.Fatal("malformed setting authorized")
	}
	if extensionAuthenticationPolicy(ext.Actor{PrimarySessionAuthorized: true}) != "primary-session-demo" || extensionAuthenticationPolicy(ext.Actor{MFA: true}) != "mfa" {
		t.Fatal("policy misreported")
	}
}

func TestPrimarySessionDemoRefreshMFARequirement(t *testing.T) {
	org := uuid.MustParse("3d89880a-f45e-4d40-a166-2e6babbc4240")
	a := &App{Config: &config.Config{}}
	claims := &middleware.JWTClaims{OrganizationID: org, MFAAt: jwt.NewNumericDate(time.Now())}
	if !a.extensionRefreshRequiresMFA(claims) {
		t.Fatal("production MFA gate lost")
	}
	a.Config.Extensions.PrimarySessionDemoOrganizationID = org.String()
	if a.extensionRefreshRequiresMFA(claims) {
		t.Fatal("demo refresh still blocked on factor")
	}
	a.Config.Extensions.PrimarySessionDemoOrganizationID = uuid.NewString()
	if !a.extensionRefreshRequiresMFA(claims) {
		t.Fatal("wrong org relaxed")
	}
}
