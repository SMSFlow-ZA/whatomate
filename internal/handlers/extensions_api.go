package handlers

import (
	"context"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	"github.com/zerodha/fastglue"
)

// ExtensionDomainHandler is the only bridge from HTTP auth into add-on context.
// Every request reloads exact scopes intersected with live host role permissions.
func (a *App) ExtensionDomainHandler(r *fastglue.Request) error {
	path := string(r.RequestCtx.Path())
	prefix := "/api/extensions/"
	if !strings.HasPrefix(path, prefix) {
		return r.SendErrorEnvelope(404, "Not found", nil, "")
	}
	module := strings.SplitN(strings.TrimPrefix(path, prefix), "/", 2)[0]
	handler := a.ExtensionAPI[module]
	if handler == nil || a.ExtensionAccess == nil {
		return r.SendErrorEnvelope(404, "Module unavailable", nil, "")
	}
	limit := 30 * time.Second
	if module == "ai" {
		limit = 2 * time.Minute
	}
	opctx, cancel := context.WithTimeout(context.WithoutCancel(r.RequestCtx), limit)
	defer cancel()
	claims, _ := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims)
	opctx = context.WithValue(opctx, extensionClaimsKey{}, claims)
	actor, e := a.ExtensionActor(r)
	if e != nil {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Verified extension session required", nil, "extension_denied")
	}
	trusted, e := a.ExtensionAccess.Load(opctx, addonapi.Actor{OrganizationID: actor.OrganizationID, UserID: actor.UserID, SessionID: actor.SessionID, MFA: actor.MFA, PrimarySessionAuthorized: actor.PrimarySessionAuthorized, Permissions: actor.Permissions})
	if e != nil {
		return r.SendErrorEnvelope(503, "Extension authorization unavailable", nil, "")
	}
	fasthttpadaptor.NewFastHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Domain handlers return bounded, non-streaming responses. Revalidate the
		// original session after long operations, before any protected bytes leave.
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req.WithContext(addonapi.WithActor(opctx, trusted)))
		if current, err := a.refreshExtensionActor(opctx, trusted); err != nil || !sameExtensionAccess(trusted, current) {
			addonapi.Error(w, http.StatusForbidden, "extension_session_changed")
			return
		}
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))(r.RequestCtx)
	return nil
}

func (a *App) RegisterExtensionDomainRoutes(g *fastglue.Fastglue) {
	g.GET("/api/extensions/catalog", a.ExtensionCatalog)
	for _, module := range []string{"directory", "cases", "knowledge", "ai", "reports", "governance", "conversation"} {
		path := "/api/extensions/" + module + "/{path:*}"
		g.GET(path, a.ExtensionDomainHandler)
		g.POST(path, a.ExtensionDomainHandler)
		g.PUT(path, a.ExtensionDomainHandler)
		g.DELETE(path, a.ExtensionDomainHandler)
	}
}

func (a *App) ExtensionCatalog(r *fastglue.Request) error {
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	opctx, cancel := context.WithTimeout(context.WithoutCancel(r.RequestCtx), 5*time.Second)
	defer cancel()
	actor, e := a.ExtensionActor(r)
	if e != nil {
		return r.SendErrorEnvelope(403, "Verified extension session required", nil, "extension_denied")
	}
	if a.ExtensionRegistry == nil || a.ExtensionAccess == nil {
		return r.SendErrorEnvelope(503, "Module catalog unavailable", nil, "")
	}
	trusted, e := a.ExtensionAccess.Load(opctx, addonapi.Actor{OrganizationID: actor.OrganizationID, UserID: actor.UserID, SessionID: actor.SessionID, MFA: actor.MFA, PrimarySessionAuthorized: actor.PrimarySessionAuthorized, Permissions: actor.Permissions})
	if e != nil {
		return r.SendErrorEnvelope(503, "Scoped permissions unavailable", nil, "")
	}
	permissions := append([]string{}, trusted.Permissions...)
	grants := append([]addonapi.Grant{}, trusted.Grants...)
	catalog := map[string]any{"modules": a.ExtensionRegistry.Manifest(), "permissions": permissions, "grants": grants, "mfa_verified": actor.MFA, "primary_session_authorized": actor.PrimarySessionAuthorized, "authentication_policy": extensionAuthenticationPolicy(actor)}
	if disclosure, present := a.extensionCatalogAIProcessing(trusted); present {
		catalog["ai_processing"] = disclosure
	}
	return r.SendEnvelope(catalog)
}
