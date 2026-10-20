package handlers

import "strings"

// General RBAC delegates only known compiled boundaries. Their handlers enforce
// primary/MFA/current exact grants themselves; this is not an administrator or
// wildcard extension permission. Unknown families remain denied by AuthorizeAPI.
func (a *App) extensionOwnsAuthorization(path, method string) bool {
	if path == "/api/extensions/catalog" {
		return method == "GET"
	}
	if strings.HasPrefix(path, "/api/extensions/security/") {
		switch path {
		case "/api/extensions/security/mfa":
			return method == "GET"
		case "/api/extensions/security/mfa/enroll", "/api/extensions/security/mfa/confirm", "/api/extensions/security/mfa/challenge", "/api/extensions/security/mfa/revoke", "/api/extensions/security/emergency/request", "/api/extensions/security/emergency/approve", "/api/extensions/security/emergency/use", "/api/extensions/security/emergency/revoke":
			return method == "POST"
		}
		return false
	}
	if !strings.HasPrefix(path, "/api/extensions/") {
		return false
	}
	module := strings.SplitN(strings.TrimPrefix(path, "/api/extensions/"), "/", 2)[0]
	if a.ExtensionAPI[module] == nil {
		return false
	}
	return method == "GET" || method == "POST" || method == "PUT" || method == "DELETE"
}
