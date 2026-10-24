// Package channels is the versioned, storage-independent provider boundary.
// Providers must not depend on the host's internal models or database handles.
package channels

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const APIVersion = 1
const (
	StatusQueued    = "queued"
	StatusSubmitted = "submitted"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
	StatusUnknown   = "unknown"
)

// Credentials must never be returned from a host API or written to logs.
type Credentials map[string]string

func (Credentials) String() string   { return "[redacted credentials]" }
func (Credentials) GoString() string { return "[redacted credentials]" }

type Descriptor struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
	Channel    string `json:"channel"`
}
type Capabilities struct {
	Text                 bool `json:"text"`
	Inbound              bool `json:"inbound"`
	DeliveryReports      bool `json:"delivery_reports"`
	Reconciliation       bool `json:"reconciliation"`
	IdempotentSubmission bool `json:"idempotent_submission"`
}
type Connection struct {
	ID          string            `json:"id"`
	Config      map[string]string `json:"config"`
	Credentials Credentials       `json:"-"`
}
type Balance struct {
	Amount   string `json:"amount"` // Decimal string; never infer ZAR from a credit count.
	Unit     string `json:"unit"`   // currency or credits
	Currency string `json:"currency,omitempty"`
}
type ConnectionInfo struct {
	Capabilities Capabilities `json:"capabilities"`
	Balance      *Balance     `json:"balance,omitempty"`
}
type Message struct {
	ClientMessageID string `json:"client_message_id"`
	Destination     string `json:"destination"`
	Text            string `json:"text"`
	CheckOptOuts    bool   `json:"check_opt_outs"`
}
type Submission struct {
	ClientMessageID   string `json:"client_message_id"`
	ProviderMessageID string `json:"provider_message_id,omitempty"`
	Status            string `json:"status"`
	Code              string `json:"code,omitempty"`
}
type Event struct {
	ID                string    `json:"id"`
	Kind              string    `json:"kind"` // status or inbound
	ClientMessageID   string    `json:"client_message_id"`
	ProviderMessageID string    `json:"provider_message_id,omitempty"`
	Status            string    `json:"status,omitempty"`
	Code              string    `json:"code,omitempty"`
	Phone             string    `json:"phone,omitempty"`
	Text              string    `json:"text,omitempty"`
	OptOut            bool      `json:"opt_out"`
	OccurredAt        time.Time `json:"occurred_at"`
}
type EventPage struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// The host durably claims each attempt before calling Send and never calls Send
// again for an uncertain/expired claim. Providers must not automatically retry a
// submission unless their transport guarantees durable replay. Advertise that
// guarantee truthfully in IdempotentSubmission. A transport timeout is unknown,
// not proof that no SMS was submitted. Poll is replayable; the host commits its
// cursor only after persisting every event in the page. Providers without a
// lookup API return unknown from Reconcile and recover through delivery reports.
type Provider interface {
	Descriptor() Descriptor
	Validate(context.Context, Connection) (ConnectionInfo, error)
	Send(context.Context, Connection, Message) (Submission, error)
	Reconcile(context.Context, Connection, string) (Submission, error)
	Poll(context.Context, Connection, string) (EventPage, error)
}

// ProviderError contains a safe, bounded machine code, never a raw HTTP body.
type ProviderError struct {
	Kind string
	Code string
}

func (e *ProviderError) Error() string { return "channel provider: " + e.Kind }

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }
func (r *Registry) Register(p Provider) error {
	if p == nil {
		return errors.New("nil provider")
	}
	d := p.Descriptor()
	if d.ID == "" || d.Version == "" || d.APIVersion != APIVersion || d.Channel != "sms" {
		return errors.New("incompatible channel provider")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[d.ID]; ok {
		return fmt.Errorf("duplicate provider %q", d.ID)
	}
	r.providers[d.ID] = p
	return nil
}
func (r *Registry) Get(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type SMSPreview struct {
	Encoding string `json:"encoding"`
	Units    int    `json:"units"`
	Segments int    `json:"segments"`
}

// PreviewSMS counts GSM extension characters as two septets and supplementary
// Unicode characters as two UTF-16 units. It never trims or changes the text.
func PreviewSMS(text string, maxSegments int) (SMSPreview, error) {
	if maxSegments < 1 || maxSegments > 10 || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return SMSPreview{}, errors.New("invalid SMS content or segment limit")
	}
	const basic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	const extended = "\f^{}\\[~]|€"
	units := 0
	gsm := true
	for _, r := range text {
		if strings.ContainsRune(basic, r) {
			units++
		} else if strings.ContainsRune(extended, r) {
			units += 2
		} else {
			gsm = false
			break
		}
	}
	preview := SMSPreview{Encoding: "GSM-7", Units: units, Segments: 1}
	single, multi := 160, 153
	if !gsm {
		preview.Encoding = "UTF-16"
		preview.Units = len(utf16.Encode([]rune(text)))
		single, multi = 70, 67
	}
	if preview.Units > single {
		preview.Segments = (preview.Units + multi - 1) / multi
	}
	if preview.Segments > maxSegments {
		return preview, errors.New("SMS exceeds configured segment limit")
	}
	return preview, nil
}
