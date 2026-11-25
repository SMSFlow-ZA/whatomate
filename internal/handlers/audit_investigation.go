package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

var errAuditNotFound = errors.New("audit record not found")

type auditReadRow struct {
	models.AuditLog
	ResourceAvailable bool
	ActorAvailable    bool
}

func auditResponse(row auditReadRow, v auditViewer) AuditLogResponse {
	entry := row.AuditLog
	response := AuditLogResponse{ID: entry.ID, ResourceType: entry.ResourceType, ResourceID: entry.ResourceID, UserID: entry.UserID, UserName: entry.UserName, Action: entry.Action, Changes: audit.SanitizeChanges(entry.Changes), CreatedAt: entry.CreatedAt, ResourceAvailable: row.ResourceAvailable, ActorAvailable: row.ActorAvailable, AuditContext: entry.AuditContext}
	switch entry.ContextVersion {
	case 1:
		response.ContextAvailable = true
		response.ContextState = "captured"
	case 0:
		response.ContextState = "legacy"
		response.AuditContext = models.AuditContext{Category: "configuration"}
	default:
		response.ContextState = "unsupported"
		response.AuditContext = models.AuditContext{ContextVersion: entry.ContextVersion, Category: "unavailable"}
	}
	if entry.UserID == uuid.Nil {
		response.UserName = "Actor unavailable"
		if response.ContextAvailable {
			response.UserName = "Unverified actor"
			if entry.Event == string(audit.IdentityProvisioned) {
				response.UserName = "Provisioning policy"
			}
		}
	} else if entry.UserID != v.User && !v.has(models.ResourceUsers, models.ActionRead) {
		response.UserID = uuid.Nil
		response.UserName = "Staff member"
		response.ActorMasked = true
	}
	if response.ContextAvailable && !v.has(models.ResourceUsers, models.ActionRead) && response.OriginIP != "" {
		response.OriginIP, response.OriginSource = "", ""
		response.OriginMasked = true
	}
	if entry.ResourceType == "inbox_case" && !v.has(models.ResourceContacts, models.ActionRead) {
		response.Changes = models.JSONBArray{}
	}
	return response
}

func auditReadIdentity(r *fastglue.Request, a *App) (uuid.UUID, uuid.UUID, *middleware.JWTClaims, error) {
	org, user, err := a.getOrgAndUserID(r)
	if err != nil {
		return org, user, nil, errAuditUnauthenticated
	}
	if a.DB == nil {
		return org, user, nil, errAuditStorage
	}
	claims, _ := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims)
	return org, user, claims, nil
}

func (a *App) auditReadError(r *fastglue.Request, org, user uuid.UUID, err error, action string) error {
	switch {
	case errors.Is(err, errAuditUnauthenticated):
		return r.SendErrorEnvelope(401, "Sign in again to view audit records", nil, "audit_identity_unavailable")
	case errors.Is(err, errAuditForbidden):
		a.observeDenial(r, org, user, models.ResourceAuditLogs, action)
		return r.SendErrorEnvelope(403, "You do not have permission for this audit action", nil, "audit_access_denied")
	case errors.Is(err, errAuditInput):
		return r.SendErrorEnvelope(400, "Check audit dates, identifiers, filters and pagination", nil, "audit_invalid_filters")
	case errors.Is(err, errAuditNotFound):
		return r.SendErrorEnvelope(404, "Audit log not found", nil, "")
	case errors.Is(err, errAuditChanged):
		return r.SendErrorEnvelope(409, "Audit access or results changed; refresh before exporting", nil, "audit_changed")
	case errors.Is(err, errAuditExportSize):
		return r.SendErrorEnvelope(422, "This export page is too large; reduce its limit or narrow the date range", nil, "audit_export_too_large")
	default:
		requestID := a.requestAuditContext(r).RequestID.String()
		a.Log.Error("Audit investigation unavailable", "code", audit.ErrorCode(err), "request_id", requestID)
		return r.SendErrorEnvelope(503, "Audit records are temporarily unavailable; try again", map[string]string{"request_id": requestID}, "audit_unavailable")
	}
}

func requireAuditRead(db *gorm.DB, org, user uuid.UUID, claims *middleware.JWTClaims, export bool) (auditViewer, error) {
	v, err := loadAuditViewer(db, org, user, claims)
	if err != nil {
		return v, err
	}
	if !v.has(models.ResourceAuditLogs, models.ActionRead) || (export && !v.has(models.ResourceAuditLogs, models.ActionExport)) {
		return v, errAuditForbidden
	}
	return v, nil
}

func (a *App) authorizeAuditRoute(r *fastglue.Request, export bool) bool {
	org, user, claims, err := auditReadIdentity(r, a)
	action := models.ActionRead
	if export {
		action = models.ActionExport
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = requireAuditRead(privateInboxIntegrationDB(a.DB).WithContext(ctx), org, user, claims, export)
	}
	if err != nil {
		_ = a.auditReadError(r, org, user, err, action)
		return false
	}
	return true
}

func queryAuditRows(q *gorm.DB, v auditViewer, limit, offset int) ([]auditReadRow, error) {
	rows := []auditReadRow{}
	expression, args := auditAvailability(v)
	// Historical diffs predate today's write budget. Bound their database
	// projection before decoding or buffering a page; never rewrite the row.
	const columns = "audit_logs.id,audit_logs.organization_id,audit_logs.resource_type,audit_logs.resource_id,audit_logs.user_id,audit_logs.user_name,audit_logs.action,audit_logs.created_at,audit_logs.context_version,audit_logs.category,audit_logs.event,audit_logs.outcome,audit_logs.reason,audit_logs.permission,audit_logs.request_id,audit_logs.origin_ip,audit_logs.origin_source,audit_logs.actor_verified,audit_logs.account_id,audit_logs.case_id"
	const changes = "CASE WHEN jsonb_typeof(audit_logs.changes)='array' AND octet_length(audit_logs.changes::text)<=65536 THEN audit_logs.changes ELSE jsonb_build_array(jsonb_build_object('field','changes_unavailable','old_value',NULL,'new_value','[unavailable]')) END AS changes"
	actor := "false AS actor_available"
	if v.has(models.ResourceUsers, models.ActionRead) {
		actor = "EXISTS (SELECT 1 FROM users actor_target WHERE actor_target.id=audit_logs.user_id AND actor_target.organization_id=? AND actor_target.deleted_at IS NULL) AS actor_available"
		args = append(args, v.Org)
	}
	err := q.Select(columns+","+changes+","+expression+","+actor, args...).Order("audit_logs.created_at DESC,audit_logs.id DESC").Offset(offset).Limit(limit).Scan(&rows).Error
	return rows, err
}

func (a *App) listScopedAuditLogs(r *fastglue.Request) error {
	org, user, claims, err := auditReadIdentity(r, a)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	filters, err := parseAuditFilters(r, false)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var viewer auditViewer
	var position auditCursor
	var rows []auditReadRow
	var total int64
	err = privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		viewer, err = requireAuditRead(tx, org, user, claims, false)
		if err != nil {
			return err
		}
		position, err = filters.position(viewer, time.Now())
		if err != nil {
			return err
		}
		base := filters.apply(scopeAuditLogs(tx, viewer)).Where("audit_logs.created_at<=?", position.AsOf)
		if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return err
		}
		rows, err = queryAuditRows(position.apply(filters.apply(scopeAuditLogs(tx, viewer))), viewer, filters.Limit+1, (filters.Page-1)*filters.Limit)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	more := len(rows) > filters.Limit
	if more {
		rows = rows[:filters.Limit]
	}
	response := make([]AuditLogResponse, len(rows))
	for i, row := range rows {
		response[i] = auditResponse(row, viewer)
	}
	next := ""
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = position.next(last.CreatedAt, last.ID)
	}
	return r.SendEnvelope(map[string]any{"audit_logs": response, "total": total, "page": filters.Page, "limit": filters.Limit, "total_pages": (total + int64(filters.Limit) - 1) / int64(filters.Limit), "has_more": more, "next_cursor": next, "as_of": position.AsOf, "capabilities": auditCapabilities(viewer)})
}

func (a *App) getScopedAuditLog(r *fastglue.Request) error {
	org, user, claims, err := auditReadIdentity(r, a)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	rawID, _ := r.RequestCtx.UserValue("id").(string)
	id, parseErr := uuid.Parse(rawID)
	if parseErr != nil || id == uuid.Nil {
		return a.auditReadError(r, org, user, errAuditInput, models.ActionRead)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var viewer auditViewer
	var rows []auditReadRow
	err = privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		viewer, err = requireAuditRead(tx, org, user, claims, false)
		if err != nil {
			return err
		}
		rows, err = queryAuditRows(scopeAuditLogs(tx, viewer).Where("audit_logs.id=?", id), viewer, 1, 0)
		if err == nil && len(rows) == 0 {
			return errAuditNotFound
		}
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	return r.SendEnvelope(auditResponse(rows[0], viewer))
}

func auditCapabilities(v auditViewer) map[string]bool {
	return map[string]bool{"export": v.has(models.ResourceAuditLogs, models.ActionExport), "actor_details": v.has(models.ResourceUsers, models.ActionRead)}
}

// ListAuditLogFilters derives choices from readable records, not the directory.
func (a *App) ListAuditLogFilters(r *fastglue.Request) error {
	org, user, claims, err := auditReadIdentity(r, a)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	f, err := parseAuditFilters(r, false)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	if f.Cursor != "" || f.Page != 1 {
		return a.auditReadError(r, org, user, errAuditInput, models.ActionRead)
	}
	asOf := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type actorChoice struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	actors := []actorChoice{}
	resources, events, outcomes := []string{}, []string{}, []string{}
	var viewer auditViewer
	err = privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		viewer, err = requireAuditRead(tx, org, user, claims, false)
		if err != nil {
			return err
		}
		base := func() *gorm.DB { return f.apply(scopeAuditLogs(tx, viewer)).Where("audit_logs.created_at<=?", asOf) }
		if err := base().Distinct("audit_logs.resource_type").Order("audit_logs.resource_type").Limit(64).Pluck("audit_logs.resource_type", &resources).Error; err != nil {
			return err
		}
		if err := base().Where("audit_logs.context_version=1 AND audit_logs.event<>''").Distinct("audit_logs.event").Order("audit_logs.event").Limit(64).Pluck("audit_logs.event", &events).Error; err != nil {
			return err
		}
		if err := base().Select("DISTINCT CASE WHEN audit_logs.context_version=1 AND audit_logs.outcome<>'' THEN audit_logs.outcome ELSE 'unavailable' END AS value").Order("value").Limit(64).Scan(&outcomes).Error; err != nil {
			return err
		}
		q := base()
		if !viewer.has(models.ResourceUsers, models.ActionRead) {
			q = q.Where("audit_logs.user_id IN ?", []uuid.UUID{viewer.User, uuid.Nil})
		}
		return q.Select("audit_logs.user_id AS id,max(audit_logs.user_name) AS name").Group("audit_logs.user_id").Order("audit_logs.user_id").Limit(101).Scan(&actors).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionRead)
	}
	limited := len(actors) > 100
	if limited {
		actors = actors[:100]
	}
	for i := range actors {
		if actors[i].ID == uuid.Nil {
			actors[i].Name = "System or unverified actor"
		} else if !viewer.has(models.ResourceUsers, models.ActionRead) {
			actors[i].Name = "You"
		}
	}
	return r.SendEnvelope(map[string]any{"resources": resources, "events": events, "outcomes": outcomes, "actors": actors, "actor_choices_limited": limited, "capabilities": auditCapabilities(viewer)})
}

func auditCSVCell(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\ufeff' })
	if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func encodeAuditCSV(rows []auditReadRow, viewer auditViewer) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&buffer)
	w.UseCRLF = true
	if err := w.Write([]string{"Recorded at (UTC)", "Audit record ID", "Resource", "Resource ID", "Actor", "Actor ID", "Action", "Category", "Event", "Outcome", "Reason", "Request ID", "Origin IP", "Origin source", "Account ID", "Case ID", "Context", "Changes"}); err != nil {
		return nil, err
	}
	idText := func(id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		return id.String()
	}
	for _, row := range rows {
		e := auditResponse(row, viewer)
		changes, err := json.Marshal(e.Changes)
		if err != nil {
			return nil, err
		}
		actorID := ""
		if e.UserID != uuid.Nil {
			actorID = e.UserID.String()
		}
		cells := []string{e.CreatedAt.UTC().Format(time.RFC3339Nano), e.ID.String(), e.ResourceType, e.ResourceID.String(), e.UserName, actorID, string(e.Action), e.Category, e.Event, e.Outcome, e.Reason, e.RequestID, e.OriginIP, e.OriginSource, idText(e.AccountID), idText(e.CaseID), e.ContextState, string(changes)}
		for i := range cells {
			cells[i] = auditCSVCell(cells[i])
		}
		if err := w.Write(cells); err != nil {
			return nil, err
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return nil, err
		}
		if buffer.Len() > 4<<20 {
			return nil, errAuditExportSize
		}
	}
	w.Flush()
	return buffer.Bytes(), w.Error()
}

// ExportAuditLogs produces one bounded cursor page. Generation is audited before
// any CSV bytes are returned; it does not claim that the client received the file.
func (a *App) ExportAuditLogs(r *fastglue.Request) error {
	org, user, claims, err := auditReadIdentity(r, a)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionExport)
	}
	f, err := parseAuditFilters(r, true)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionExport)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := privateInboxIntegrationDB(a.DB).WithContext(ctx)
	var viewer auditViewer
	var position auditCursor
	var rows []auditReadRow
	err = db.Transaction(func(tx *gorm.DB) error {
		var err error
		viewer, err = requireAuditRead(tx, org, user, claims, true)
		if err != nil {
			return err
		}
		position, err = f.position(viewer, time.Now())
		if err != nil {
			return err
		}
		rows, err = queryAuditRows(position.apply(f.apply(scopeAuditLogs(tx, viewer))), viewer, f.Limit+1, 0)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionExport)
	}
	more := len(rows) > f.Limit
	if more {
		rows = rows[:f.Limit]
	}
	data, err := encodeAuditCSV(rows, viewer)
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionExport)
	}
	ids := make([]uuid.UUID, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		fresh, err := requireAuditRead(tx, org, user, claims, true)
		if err != nil {
			return err
		}
		if fresh.fingerprint() != viewer.fingerprint() {
			return errAuditChanged
		}
		if len(ids) > 0 {
			var readable int64
			if err := f.apply(scopeAuditLogs(tx, fresh)).Where("audit_logs.id IN ?", ids).Count(&readable).Error; err != nil {
				return err
			}
			if readable != int64(len(ids)) {
				return errAuditChanged
			}
		}
		return audit.RecordSecurity(tx, audit.SecurityEvent{OrganizationID: org, ActorID: user, ResourceType: "audit_logs", ResourceID: org, Kind: audit.Exported, Outcome: audit.Success, Reason: audit.ExportPrepared, Context: a.requestAuditContext(r)})
	})
	if err != nil {
		return a.auditReadError(r, org, user, err, models.ActionExport)
	}
	next := ""
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = position.next(last.CreatedAt, last.ID)
	}
	r.RequestCtx.Response.Header.Set("Content-Type", "text/csv; charset=utf-8")
	r.RequestCtx.Response.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="whatomate-audit-%s.csv"`, time.Now().UTC().Format("20060102-150405")))
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	r.RequestCtx.Response.Header.Set("Pragma", "no-cache")
	r.RequestCtx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	r.RequestCtx.Response.Header.Set("X-Audit-Export-Rows", strconv.Itoa(len(rows)))
	r.RequestCtx.Response.Header.Set("X-Audit-Has-More", strconv.FormatBool(more))
	r.RequestCtx.Response.Header.Set("X-Audit-Next-Cursor", next)
	r.RequestCtx.Response.SetStatusCode(200)
	r.RequestCtx.Response.SetBody(data)
	return nil
}
