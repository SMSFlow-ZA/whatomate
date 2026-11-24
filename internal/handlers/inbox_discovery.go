package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

const inboxDiscoveryVersion = 1
const inboxPriorityOrder = "CASE inbox_cases.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END"
const inboxActivityOrder = "GREATEST(inbox_cases.opened_at,inbox_cases.last_inbound_at,inbox_cases.last_human_reply_at)"
const inboxUnansweredOrder = "CASE WHEN inbox_cases.resolved_at IS NOT NULL OR inbox_cases.status='resolved' OR inbox_cases.last_inbound_at IS NULL THEN NULL WHEN inbox_cases.first_human_reply_at IS NULL THEN inbox_cases.opened_at ELSE inbox_cases.next_reply_from END"

func normalizeInboxDiscovery(value models.InboxQueueDiscovery) (models.InboxQueueDiscovery, error) {
	if value.Order == "" {
		value.Order = "priority_waiting"
	}
	switch value.Order {
	case "priority_waiting", "latest_activity", "longest_unanswered", "newest", "oldest", "nearest_deadline":
	default:
		return value, errInboxTransition
	}
	hasDates := false
	for _, boundary := range []**time.Time{&value.OpenedFrom, &value.OpenedBefore, &value.ActivityFrom, &value.ActivityBefore} {
		if *boundary == nil {
			continue
		}
		timestamp := **boundary
		if timestamp.IsZero() || timestamp.Year() < 1 || timestamp.Year() > 9999 || timestamp.Nanosecond()%1000 != 0 {
			return value, errInboxTransition
		}
		canonical := timestamp.UTC()
		*boundary = &canonical
		hasDates = true
	}
	if hasDates && value.Timezone == "" {
		return value, errInboxTransition
	}
	if value.Timezone != "" {
		if len(value.Timezone) > 128 || value.Timezone == "Local" {
			return value, errInboxTransition
		}
		if _, err := time.LoadLocation(value.Timezone); err != nil {
			return value, errInboxTransition
		}
	}
	if value.OpenedFrom != nil && value.OpenedBefore != nil && !value.OpenedFrom.Before(*value.OpenedBefore) {
		return value, errInboxTransition
	}
	if value.ActivityFrom != nil && value.ActivityBefore != nil && !value.ActivityFrom.Before(*value.ActivityBefore) {
		return value, errInboxTransition
	}
	return value, nil
}

func applyInboxDiscoveryDates(q *gorm.DB, discovery models.InboxQueueDiscovery) *gorm.DB {
	if discovery.OpenedFrom != nil {
		q = q.Where("inbox_cases.opened_at>=?", *discovery.OpenedFrom)
	}
	if discovery.OpenedBefore != nil {
		q = q.Where("inbox_cases.opened_at<?", *discovery.OpenedBefore)
	}
	if discovery.ActivityFrom != nil {
		q = q.Where(inboxActivityOrder+">=?", *discovery.ActivityFrom)
	}
	if discovery.ActivityBefore != nil {
		q = q.Where(inboxActivityOrder+"<?", *discovery.ActivityBefore)
	}
	return q
}

// Current, incomplete, unpaused obligations only; every identity must agree.
// This aggregation is applied before ordering and paging, not to the final50 rows.
func inboxDiscoveryDeadlineJoin(q *gorm.DB, org uuid.UUID) *gorm.DB {
	return q.Joins("LEFT JOIN (SELECT organization_id,account_id,case_id,policy_id,MIN(deadline) AS next_due FROM inbox_sla_obligations WHERE organization_id=? AND completed_at IS NULL AND NOT paused AND deadline IS NOT NULL AND deleted_at IS NULL GROUP BY organization_id,account_id,case_id,policy_id) queue_due ON queue_due.organization_id=inbox_cases.organization_id AND queue_due.account_id=inbox_cases.account_id AND queue_due.case_id=inbox_cases.id AND queue_due.policy_id=inbox_cases.sla_policy_id AND inbox_cases.sla_projection_version=inbox_cases.version AND inbox_cases.resolved_at IS NULL AND inbox_cases.status<>'resolved'", org)
}

func inboxDiscoveryExpression(order string) (expression string, descending bool, nullable bool) {
	switch order {
	case "latest_activity":
		return inboxActivityOrder, true, false
	case "longest_unanswered":
		return inboxUnansweredOrder, false, true
	case "newest":
		return "inbox_cases.opened_at", true, false
	case "nearest_deadline":
		return "queue_due.next_due", false, true
	default:
		return "inbox_cases.opened_at", false, false
	}
}

func orderInboxDiscovery(q *gorm.DB, discovery models.InboxQueueDiscovery, org uuid.UUID) (*gorm.DB, string) {
	if discovery.Order == "nearest_deadline" {
		q = inboxDiscoveryDeadlineJoin(q, org)
	}
	expression, descending, nullable := inboxDiscoveryExpression(discovery.Order)
	if discovery.Order == "priority_waiting" {
		q = q.Order(inboxPriorityOrder)
	}
	direction := " ASC"
	if descending {
		direction = " DESC"
	}
	nulls := ""
	if nullable {
		nulls = " NULLS LAST"
	}
	return q.Order(expression + direction + nulls + ",inbox_cases.id" + direction), expression
}

type inboxDiscoveryCursor struct {
	Version  int        `json:"v"`
	Context  string     `json:"context"`
	Revision string     `json:"revision"`
	Order    string     `json:"order"`
	ID       uuid.UUID  `json:"id"`
	Rank     int        `json:"rank"`
	Key      *time.Time `json:"key"`
}

func inboxDiscoveryContext(org, user uuid.UUID, filters models.InboxQueueFilter, discovery models.InboxQueueDiscovery, view string) (string, error) {
	data, err := json.Marshal(struct {
		Org       uuid.UUID
		User      uuid.UUID
		Filters   models.InboxQueueFilter
		Discovery models.InboxQueueDiscovery
		View      string
	}{org, user, filters, discovery, view})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func readInboxDiscoveryCursor(raw, context, order string) (*inboxDiscoveryCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 1024 {
		return nil, errInboxTransition
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errInboxTransition
	}
	var cursor inboxDiscoveryCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != inboxDiscoveryVersion || cursor.Context != context || cursor.Order != order || cursor.ID == uuid.Nil {
		return nil, errInboxTransition
	}
	if len(cursor.Revision) != 64 {
		return nil, errInboxTransition
	}
	if _, err := hex.DecodeString(cursor.Revision); err != nil {
		return nil, errInboxTransition
	}
	_, _, nullable := inboxDiscoveryExpression(order)
	if (!nullable && cursor.Key == nil) || cursor.Rank < 0 || cursor.Rank > 3 || (order != "priority_waiting" && cursor.Rank != 0) {
		return nil, errInboxTransition
	}
	if cursor.Key != nil && (cursor.Key.IsZero() || cursor.Key.Year() < 1 || cursor.Key.Year() > 9999 || cursor.Key.Nanosecond()%1000 != 0) {
		return nil, errInboxTransition
	}
	return &cursor, nil
}

func pageInboxDiscovery(q *gorm.DB, discovery models.InboxQueueDiscovery, cursor *inboxDiscoveryCursor) *gorm.DB {
	if cursor == nil {
		return q
	}
	expression, descending, nullable := inboxDiscoveryExpression(discovery.Order)
	if discovery.Order == "priority_waiting" {
		return q.Where("("+inboxPriorityOrder+",inbox_cases.opened_at,inbox_cases.id)>(?,?,?)", cursor.Rank, cursor.Key, cursor.ID)
	}
	if nullable && cursor.Key == nil {
		return q.Where(expression+" IS NULL AND inbox_cases.id>?", cursor.ID)
	}
	operator := ">"
	if descending {
		operator = "<"
	}
	predicate := "(" + expression + ",inbox_cases.id)" + operator + "(?,?)"
	if nullable {
		predicate = "(" + predicate + " OR " + expression + " IS NULL)"
	}
	return q.Where(predicate, cursor.Key, cursor.ID)
}

func encodeInboxDiscoveryCursor(context string, discovery models.InboxQueueDiscovery, row inboxQueueRow, revision string) (string, error) {
	rank := 0
	if discovery.Order == "priority_waiting" {
		switch row.Priority {
		case models.InboxPriorityUrgent:
		case models.InboxPriorityHigh:
			rank = 1
		case models.InboxPriorityNormal:
			rank = 2
		default:
			rank = 3
		}
	}
	encoded, err := json.Marshal(inboxDiscoveryCursor{Version: inboxDiscoveryVersion, Context: context, Revision: revision, Order: discovery.Order, ID: row.ID, Rank: rank, Key: row.DiscoveryKey})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func parseInboxDiscovery(raw []byte) (models.InboxQueueDiscovery, error) {
	var value models.InboxQueueDiscovery
	if len(raw) > 2048 {
		return value, errInboxTransition
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &value); err != nil {
			return value, errInboxTransition
		}
	}
	return normalizeInboxDiscovery(value)
}

var errInboxDiscoveryCursor = errors.New("invalid queue discovery or cursor")
var errInboxQueueChanged = errors.New("queue work changed while paging; refresh from the first page")

// This is a freshness marker, never an access token. Two independent aggregate
// checksums avoid collecting every case ID in memory or returning hidden fields.
// Only current, scoped matching case identities/work/sort keys participate. In
// particular, contact-wide activity on a different account is not a signal.
func inboxDiscoveryRevision(q *gorm.DB, discovery models.InboxQueueDiscovery, org uuid.UUID) (string, error) {
	if discovery.Order == "nearest_deadline" {
		q = inboxDiscoveryDeadlineJoin(q, org)
	}
	expression, _, _ := inboxDiscoveryExpression(discovery.Order)
	rowValue := "concat_ws('|',inbox_cases.id::text,inbox_cases.version::text,inbox_cases.updated_at::text,COALESCE((" + expression + ")::text,''))"
	var value struct {
		Count int64
		A     string
		B     string
	}
	if err := q.Select("COUNT(*) AS count,COALESCE(SUM(hashtextextended(" + rowValue + ",0)),0)::text AS a,COALESCE(SUM(hashtextextended(" + rowValue + ",1)),0)::text AS b").Scan(&value).Error; err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
