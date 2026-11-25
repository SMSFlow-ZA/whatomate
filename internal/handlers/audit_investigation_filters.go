package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type auditFilters struct {
	Values      map[string]string
	From, To    time.Time
	ToExclusive bool
	Page, Limit int
	Cursor      string
}

var auditFilterKeys = map[string]bool{"resource_type": true, "resource_id": true, "user_id": true, "action": true, "from": true, "to": true, "category": true, "event": true, "outcome": true, "request_id": true, "account_id": true, "case_id": true, "page": true, "limit": true, "cursor": true}

func parseAuditFilters(r *fastglue.Request, export bool) (auditFilters, error) {
	f := auditFilters{Values: map[string]string{}, Page: 1, Limit: 50}
	if export {
		f.Limit = 1000
	}
	if len(r.RequestCtx.URI().QueryString()) > 8192 {
		return f, errAuditInput
	}
	valid := true
	seen := map[string]bool{}
	for k, v := range r.RequestCtx.QueryArgs().All() {
		key := string(k)
		if !auditFilterKeys[key] || seen[key] || len(v) > 1024 {
			valid = false
			continue
		}
		seen[key] = true
		f.Values[key] = string(v)
	}
	if !valid {
		return f, errAuditInput
	}
	for _, key := range []string{"resource_id", "user_id", "request_id", "account_id", "case_id"} {
		if raw := f.Values[key]; raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil || (id == uuid.Nil && key != "user_id") {
				return f, errAuditInput
			}
			f.Values[key] = id.String()
		}
	}
	if resource := f.Values["resource_type"]; resource != "" {
		known := resource == "inbox_case"
		for _, s := range auditResourceSpecs {
			known = known || s.Type == resource
		}
		if !known {
			return f, errAuditInput
		}
	}
	for key, allowed := range map[string][]string{
		"action": {"created", "updated", "deleted", "event"}, "category": {"configuration", "security"}, "outcome": {"success", "denied", "failed", "unavailable"},
	} {
		if value := f.Values[key]; value != "" {
			ok := false
			for _, candidate := range allowed {
				ok = ok || candidate == value
			}
			if !ok {
				return f, errAuditInput
			}
		}
	}
	if event := f.Values["event"]; len(event) > 64 {
		return f, errAuditInput
	} else {
		for _, c := range event {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '_' {
				return f, errAuditInput
			}
		}
	}
	for _, key := range []string{"from", "to"} {
		if raw := f.Values[key]; raw != "" {
			t, dateErr := time.Parse(time.DateOnly, raw)
			dateOnly := dateErr == nil
			if !dateOnly {
				var err error
				t, err = time.Parse(time.RFC3339Nano, raw)
				if err != nil {
					return f, errAuditInput
				}
			}
			if t.Year() < 1 || t.Year() > 9998 {
				return f, errAuditInput
			}
			if key == "from" {
				f.From = t
			} else {
				f.To = t
				f.ToExclusive = dateOnly
				if dateOnly {
					f.To = f.To.AddDate(0, 0, 1)
				}
			}
			f.Values[key] = t.UTC().Format(time.RFC3339Nano)
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && (f.To.Before(f.From) || (f.ToExclusive && f.To.Equal(f.From))) {
		return f, errAuditInput
	}
	if raw, ok := f.Values["page"]; ok {
		if export {
			return f, errAuditInput
		}
		page, err := strconv.Atoi(raw)
		if err != nil || page < 1 || page > 10000 {
			return f, errAuditInput
		}
		f.Page = page
	}
	if raw, ok := f.Values["limit"]; ok {
		limit, err := strconv.Atoi(raw)
		maxLimit := 100
		if export {
			maxLimit = 1000
		}
		if err != nil || limit < 1 || limit > maxLimit {
			return f, errAuditInput
		}
		f.Limit = limit
	}
	f.Cursor = f.Values["cursor"]
	if f.Cursor != "" && seen["page"] {
		return f, errAuditInput
	}
	delete(f.Values, "cursor")
	delete(f.Values, "page")
	delete(f.Values, "limit")
	for key, value := range f.Values {
		if value == "" {
			delete(f.Values, key)
		}
	}
	return f, nil
}

func (f auditFilters) apply(q *gorm.DB) *gorm.DB {
	for _, key := range []string{"resource_type", "resource_id", "user_id", "action"} {
		if v := f.Values[key]; v != "" {
			q = q.Where("audit_logs."+key+"=?", v)
		}
	}
	if v := f.Values["category"]; v != "" {
		q = q.Where("audit_logs.category=?", v)
		if v == "security" {
			q = q.Where("audit_logs.context_version=1")
		}
	}
	for _, key := range []string{"event", "request_id", "case_id"} {
		if v := f.Values[key]; v != "" {
			q = q.Where("audit_logs.context_version=1 AND audit_logs."+key+"=?", v)
		}
	}
	if value := f.Values["account_id"]; value != "" {
		q = q.Where("(audit_logs.context_version=1 AND audit_logs.account_id=?) OR (audit_logs.resource_type='account' AND audit_logs.resource_id=?)", value, value)
	}
	if value := f.Values["outcome"]; value != "" {
		if value == "unavailable" {
			q = q.Where("audit_logs.context_version<>1 OR audit_logs.outcome=''")
		} else {
			q = q.Where("audit_logs.context_version=1 AND audit_logs.outcome=?", value)
		}
	}
	if !f.From.IsZero() {
		q = q.Where("audit_logs.created_at>=?", f.From)
	}
	if !f.To.IsZero() {
		if f.ToExclusive {
			q = q.Where("audit_logs.created_at<?", f.To)
		} else {
			q = q.Where("audit_logs.created_at<=?", f.To)
		}
	}
	return q
}

type auditCursor struct {
	First  bool      `json:"first,omitempty"`
	At     time.Time `json:"at"`
	ID     uuid.UUID `json:"id"`
	AsOf   time.Time `json:"as_of"`
	Filter string    `json:"filter"`
}

func (f auditFilters) signature(v auditViewer) string {
	b, _ := json.Marshal([]any{v.Org, v.User, f.Values, f.ToExclusive})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (f auditFilters) position(v auditViewer, now time.Time) (auditCursor, error) {
	position := auditCursor{AsOf: now.UTC(), Filter: f.signature(v)}
	if f.Cursor == "" {
		return position, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(f.Cursor)
	if err != nil || len(b) > 512 {
		return position, errAuditInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&position) != nil {
		return position, errAuditInput
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return position, errAuditInput
	}
	invalidBoundary := position.ID == uuid.Nil || position.At.IsZero() || position.At.After(position.AsOf)
	if position.First {
		invalidBoundary = position.ID != uuid.Nil || !position.At.IsZero()
	}
	if invalidBoundary || position.AsOf.IsZero() || position.AsOf.After(now.Add(time.Second)) || position.Filter != f.signature(v) {
		return position, errAuditInput
	}
	return position, nil
}

func (p auditCursor) next(at time.Time, id uuid.UUID) string {
	p.At, p.ID = at, id
	p.First = false
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p auditCursor) first() string {
	p.First, p.At, p.ID = true, time.Time{}, uuid.Nil
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p auditCursor) apply(q *gorm.DB) *gorm.DB {
	q = q.Where("audit_logs.created_at<=?", p.AsOf)
	if p.ID != uuid.Nil {
		q = q.Where("(audit_logs.created_at,audit_logs.id)<(?,?)", p.At, p.ID)
	}
	return q
}
