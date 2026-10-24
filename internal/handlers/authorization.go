package handlers

import (
	"strings"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// AuthorizeAPI applies a deny-by-default policy before protected handlers.
// Handlers still enforce record ownership, assigned-contact and team scoping.
func (a *App) AuthorizeAPI() fastglue.FastMiddleware {
	return func(r *fastglue.Request) *fastglue.Request {
		path, method := string(r.RequestCtx.Path()), string(r.RequestCtx.Method())
		if method == "OPTIONS" || !strings.HasPrefix(path, "/api/") {
			return r
		}
		switch path {
		case "/api/auth/login", "/api/auth/register", "/api/auth/refresh", "/api/auth/logout", "/api/auth/refresh/logout", "/api/webhook":
			return r
		case "/api/branding", "/api/branding/assets/logo", "/api/branding/assets/favicon":
			if method == "GET" {
				return r
			}
		}
		if strings.HasPrefix(path, "/api/auth/sso/") || strings.HasPrefix(path, "/api/custom-actions/redirect/") {
			return r
		}
		orgID, userID, err := a.getOrgAndUserID(r)
		if err != nil {
			_ = r.SendErrorEnvelope(401, "Unauthorized", nil, "")
			return nil
		}
		if (path == "/api/channels" && method == "GET") || (path == "/api/channels/preview" && method == "POST") {
			act := models.ActionRead
			if method == "POST" {
				act = models.ActionWrite
			}
			if a.canConsumeChannel(orgID, userID, act) {
				return r
			}
		}
		// These routes act only on the authenticated user's session or profile.
		switch path {
		case "/api/me", "/api/me/settings", "/api/me/password", "/api/me/availability", "/api/me/organizations", "/api/auth/switch-org", "/api/auth/ws-token", "/api/plan":
			return r
		}
		action := models.ActionWrite
		switch method {
		case "GET":
			action = models.ActionRead
		case "DELETE":
			action = models.ActionDelete
		}
		resource := ""
		family := strings.Split(strings.TrimPrefix(path, "/api/"), "/")[0]
		switch family {
		case "users":
			resource = models.ResourceUsers
		case "roles", "permissions":
			resource = models.ResourceRoles
		case "api-keys":
			resource = models.ResourceAPIKeys
		case "accounts", "embedded-signup":
			resource = models.ResourceAccounts
		case "templates":
			resource = models.ResourceTemplates
			if strings.HasSuffix(path, "/sync") {
				action = models.ActionSync
			}
		case "flows":
			resource = models.ResourceFlowsWhatsApp
		case "campaigns":
			resource = models.ResourceCampaigns
			for _, suffix := range []string{"/start", "/pause", "/cancel", "/retry-failed"} {
				if strings.HasSuffix(path, suffix) {
					action = models.ActionExecute
				}
			}
		case "canned-responses":
			resource = models.ResourceCannedResponses
			if strings.HasSuffix(path, "/use") {
				action = models.ActionRead
			}
		case "tags":
			resource = models.ResourceTags
		case "webhooks":
			resource = models.ResourceWebhooks
		case "custom-actions":
			resource = models.ResourceCustomActions
		case "settings":
			if strings.HasPrefix(path, "/api/settings/sso") {
				resource = models.ResourceSettingsSSO
				if method == "DELETE" {
					action = models.ActionWrite
				}
			}
		case "branding":
			resource = models.ResourceSettingsBranding
		case "channel-connections":
			resource = models.ResourceSettingsChannels
			if method == "DELETE" {
				action = models.ActionWrite
			}
		case "channels":
			resource = models.ResourceChat
			if path == "/api/channels/providers" || path == "/api/channels/health" {
				resource = models.ResourceSettingsChannels
			}
		case "organizations":
			resource = models.ResourceOrganizations
			if strings.HasPrefix(path, "/api/organizations/members") && method != "GET" {
				action = models.ActionAssign
			}
		case "org":
			resource = models.ResourceSettingsGeneral
		case "analytics", "widgets":
			resource = models.ResourceAnalytics
			if strings.HasPrefix(path, "/api/analytics/agents") {
				resource = models.ResourceAnalyticsAgents
			}
		case "audit-logs":
			resource = models.ResourceAuditLogs
		case "ivr-flows":
			resource = models.ResourceIVRFlows
		case "call-logs":
			resource = models.ResourceCallLogs
			if strings.HasSuffix(path, "/hold") || strings.HasSuffix(path, "/resume") {
				resource = models.ResourceCallTransfers
				action = models.ActionWrite
			}
			if method == "GET" && a.HasPermission(userID, models.ResourceOutgoingCalls, models.ActionRead, orgID) {
				return r
			}
		case "call-transfers":
			resource = models.ResourceCallTransfers
		case "calls":
			resource = models.ResourceOutgoingCalls
		// Catalogue setup belongs to account administration; no separate catalogue
		// permission exists in the current permission model.
		case "catalogs", "products":
			resource = models.ResourceAccounts
		case "chatbot":
			switch strings.Split(strings.TrimPrefix(path, "/api/chatbot/"), "/")[0] {
			case "settings":
				resource = models.ResourceSettingsChatbot
				// Queue agents need only the pickup feature flag. The handler
				// returns that restricted representation without configuration.
				if path == "/api/chatbot/settings" && method == "GET" && (a.HasPermission(userID, models.ResourceTransfers, models.ActionRead, orgID) || a.HasPermission(userID, models.ResourceTransfers, models.ActionPickup, orgID)) {
					return r
				}
			case "keywords":
				resource = models.ResourceChatbotKeywords
			case "flows":
				resource = models.ResourceFlowsChatbot
			case "ai-contexts":
				resource = models.ResourceChatbotAI
			case "sessions":
				resource = models.ResourceSettingsChatbot
			case "transfers":
				resource = models.ResourceTransfers
				if strings.HasSuffix(path, "/pick") {
					action = models.ActionPickup
				} else if strings.HasSuffix(path, "/assign") {
					action = models.ActionWrite
					if a.HasPermission(userID, models.ResourceTransfers, models.ActionPickup, orgID) {
						return r
					}
				} else if strings.HasSuffix(path, "/resume") && a.HasPermission(userID, models.ResourceTransfers, models.ActionPickup, orgID) {
					// ResumeFromTransfer restricts pickup-only users to their own transfer.
					return r
				}
			}
		case "messages", "media":
			resource = models.ResourceChat
		case "contacts":
			resource = models.ResourceContacts
			if strings.Contains(path, "/messages") || strings.Contains(path, "/notes") || strings.HasSuffix(path, "/mark-read") || strings.HasSuffix(path, "/session-data") {
				resource = models.ResourceChat
			}
			// Agents can read only assigned contacts; the handler scopes each query.
			if method == "GET" && resource == models.ResourceContacts && a.HasPermission(userID, models.ResourceChat, models.ActionRead, orgID) {
				return r
			}
		case "teams":
			resource = models.ResourceTeams
			if strings.Contains(path, "/members") && method != "GET" {
				action = models.ActionWrite
			}
			if method == "GET" && a.HasPermission(userID, models.ResourceChat, models.ActionRead, orgID) {
				return r
			}
		case "export", "import":
			// Import/export handlers resolve the requested table and enforce its
			// resource permission. They reject unknown tables themselves.
			return r
		}
		// Chat exposes read/write permissions; deleting one's notes is a write.
		if resource == models.ResourceChat && method != "GET" {
			action = models.ActionWrite
		}
		if resource != "" && a.HasPermission(userID, resource, action, orgID) {
			return r
		}
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
		return nil
	}
}
