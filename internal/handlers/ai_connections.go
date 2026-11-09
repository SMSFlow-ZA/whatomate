package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/aiconnections"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/aiproviders"
	"github.com/zerodha/fastglue"
)

func (a *App) InitializeAIConnections() {
	cfg := a.Config.AIConnections
	service, err := aiconnections.New(a.DB, a.Config.App.EncryptionKey, cfg.Enabled, cfg.Endpoints, nil)
	a.AIConnections = service
	if err != nil {
		a.Log.Error("Custom chatbot AI disabled: invalid endpoint policy or missing instance encryption key")
	}
}

func (a *App) ListAIConnections(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsChatbot, models.ActionRead)
	if err != nil {
		return nil
	}
	s := a.AIConnections
	if !s.Enabled() {
		return r.SendEnvelope(map[string]any{"enabled": false})
	}
	connections, bindings, err := s.List(context.Background(), org)
	if err != nil {
		return aiConnectionError(r, err, 0)
	}
	type connection struct {
		models.ChatbotAIConnection
		Ready bool `json:"ready"`
	}
	list := make([]connection, 0, len(connections))
	for _, c := range connections {
		list = append(list, connection{c, s.Ready(c)})
	}
	type account struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	accounts := []account{}
	if err := a.DB.Model(&models.WhatsAppAccount{}).Select("id", "name").Where("organization_id = ?", org).Order("name,id").Find(&accounts).Error; err != nil {
		return aiConnectionError(r, err, 0)
	}
	endpoints := s.Options()
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].ID < endpoints[j].ID })
	return r.SendEnvelope(map[string]any{"enabled": true, "connections": list, "assignments": bindings, "endpoints": endpoints, "accounts": accounts,
		"can_write": a.HasPermission(user, models.ResourceSettingsChatbot, models.ActionWrite, org)})
}

func (a *App) CreateAIConnection(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsChatbot, models.ActionWrite)
	if err != nil {
		return nil
	}
	if !a.AIConnections.Enabled() {
		return aiConnectionError(r, aiconnections.ErrOff, 0)
	}
	var in struct {
		Name       string `json:"name"`
		EndpointID string `json:"endpoint_id"`
		Model      string `json:"model"`
		Credential string `json:"credential"`
	}
	if !readAIConnectionRequest(r, &in) {
		return aiConnectionError(r, aiproviders.ErrInvalidRequest, 0)
	}
	c, err := a.AIConnections.Create(context.Background(), org, in.Name, in.EndpointID, in.Model, in.Credential)
	if err != nil {
		return aiConnectionError(r, err, 0)
	}
	a.logAudit(org, user, models.ResourceSettingsChatbotAI, c.ID, models.AuditActionCreated, nil, map[string]any{"connection_id": c.ID})
	return r.SendEnvelope(c)
}

func (a *App) changeAIConnection(r *fastglue.Request, action string) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsChatbot, models.ActionWrite)
	if err != nil {
		return nil
	}
	if a.AIConnections == nil {
		return aiConnectionError(r, aiconnections.ErrOff, 0)
	}
	id, err := parsePathUUID(r, "id", "AI connection")
	if err != nil {
		return nil
	}
	ctx := context.Background()
	s := a.AIConnections
	switch action {
	case "test":
		elapsed, err := s.Probe(ctx, org, id)
		if err != nil {
			return aiConnectionError(r, err, elapsed)
		}
		return r.SendEnvelope(map[string]any{"status": "ready", "latency_ms": elapsed.Milliseconds()})
	case "update":
		var in aiconnections.Update
		if !readAIConnectionRequest(r, &in) {
			return aiConnectionError(r, aiproviders.ErrInvalidRequest, 0)
		}
		err = s.Update(ctx, org, id, in)
	case "disable":
		err = s.Disable(ctx, org, id)
	case "delete":
		err = s.Delete(ctx, org, id)
	}
	if err != nil {
		return aiConnectionError(r, err, 0)
	}
	auditAction := models.AuditActionUpdated
	if action == "delete" {
		auditAction = models.AuditActionDeleted
	}
	a.logAudit(org, user, models.ResourceSettingsChatbotAI, id, auditAction, nil, map[string]any{"operation": action})
	return r.SendEnvelope(map[string]string{"status": "ok"})
}
func (a *App) UpdateAIConnection(r *fastglue.Request) error { return a.changeAIConnection(r, "update") }
func (a *App) TestAIConnection(r *fastglue.Request) error   { return a.changeAIConnection(r, "test") }
func (a *App) DisableAIConnection(r *fastglue.Request) error {
	return a.changeAIConnection(r, "disable")
}
func (a *App) DeleteAIConnection(r *fastglue.Request) error { return a.changeAIConnection(r, "delete") }

func (a *App) AssignAIConnection(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsChatbot, models.ActionWrite)
	if err != nil {
		return nil
	}
	if a.AIConnections == nil {
		return aiConnectionError(r, aiconnections.ErrOff, 0)
	}
	var in struct {
		AccountID      uuid.UUID  `json:"account_id"`
		ConnectionID   *uuid.UUID `json:"connection_id"`
		IncludeHistory bool       `json:"include_history"`
		IncludeContext bool       `json:"include_context"`
	}
	if !readAIConnectionRequest(r, &in) || in.ConnectionID == nil {
		return aiConnectionError(r, aiproviders.ErrInvalidRequest, 0)
	}
	if err := a.AIConnections.Assign(context.Background(), org, in.AccountID, *in.ConnectionID, in.IncludeHistory, in.IncludeContext); err != nil {
		return aiConnectionError(r, err, 0)
	}
	a.logAudit(org, user, models.ResourceSettingsChatbotAI, org, models.AuditActionUpdated, nil, map[string]any{
		"account_id": in.AccountID, "connection_id": in.ConnectionID, "include_history": in.IncludeHistory, "include_context": in.IncludeContext})
	return r.SendEnvelope(map[string]string{"status": "ok"})
}

func readAIConnectionRequest(r *fastglue.Request, out any) bool {
	body := r.RequestCtx.PostBody()
	if len(body) > 8192 {
		return false
	}
	body = bytes.TrimSpace(body)
	return len(body) > 0 && body[0] == '{' && json.Unmarshal(body, out) == nil
}

func aiErrorCategory(err error) string {
	for _, known := range []error{aiconnections.ErrOff, aiconnections.ErrMissing, aiconnections.ErrAssigned, aiconnections.ErrNotReady, aiconnections.ErrChanged, aiconnections.ErrBusy,
		aiproviders.ErrEndpoint, aiproviders.ErrInvalidRequest, aiproviders.ErrTimeout, aiproviders.ErrCredential, aiproviders.ErrAccessDenied, aiproviders.ErrRateLimited, aiproviders.ErrUnavailable, aiproviders.ErrResponse} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "ai_connection_unavailable"
}
func aiConnectionError(r *fastglue.Request, err error, elapsed time.Duration) error {
	code := aiErrorCategory(err)
	status, message := 503, "AI connection unavailable"
	switch code {
	case "ai_connection_not_found":
		status, message = 404, "AI connection or account not found"
	case "ai_connection_assigned":
		status, message = 409, "Remove all assignments before replacing or deleting this connection"
	case "ai_connection_changed":
		status, message = 409, "The connection changed during the test. Test it again."
	case "ai_connection_not_ready":
		status, message = 422, "Enable and test the connection before assigning it"
	case "endpoint_not_allowed":
		status, message = 422, "The approved endpoint could not be reached under its network policy"
	case "invalid_request":
		status, message = 422, "Check the connection fields and deployment model"
	case "authentication_failed":
		status, message = 422, "The provider rejected the connection credential"
	case "access_denied":
		status, message = 422, "The provider denied access. Check endpoint network access and the credential."
	case "invalid_response":
		status, message = 422, "The provider did not return a compatible text response"
	case "ai_connection_busy", "rate_limited":
		status, message = 429, "The AI connection is busy or rate limited"
	case "timeout":
		status, message = 504, "The AI provider timed out"
	}
	return r.SendErrorEnvelope(status, message, map[string]any{"category": code, "latency_ms": elapsed.Milliseconds()}, fastglue.ErrorType(code))
}
