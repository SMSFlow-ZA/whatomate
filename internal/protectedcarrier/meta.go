package protectedcarrier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Credentials are resolved from an exact trusted account binding at dispatch.
// There is no request-controlled destination or configurable provider origin.
type Credentials struct{ PhoneID, APIVersion, AccessToken string }

// DiagnosticStage identifies a bounded transport step, never provider text.
type DiagnosticStage string

const (
	DiagnosticMessage     DiagnosticStage = "message"
	DiagnosticCredentials DiagnosticStage = "credentials"
	DiagnosticRequest     DiagnosticStage = "request"
	DiagnosticTransport   DiagnosticStage = "transport"
	DiagnosticResponse    DiagnosticStage = "response"
	DiagnosticAccepted    DiagnosticStage = "accepted"
)

// Diagnostic deliberately excludes identifiers, URLs, content and credentials.
// Provider errors contribute only positive integer codes, never their messages.
type Diagnostic struct {
	Stage        DiagnosticStage
	HTTPStatus   int
	ErrorCode    int64
	ErrorSubcode int64
}

type MetaSender struct {
	Resolve   func(context.Context, delivery.Message) (Credentials, error)
	Transport http.RoundTripper
	// ObserveDiagnostic is optional. Without it, the sender emits no diagnostics.
	ObserveDiagnostic func(Diagnostic)
}

func (s MetaSender) observe(stage DiagnosticStage, status int, payload []byte) {
	if s.ObserveDiagnostic == nil {
		return
	}
	d := Diagnostic{Stage: stage, HTTPStatus: status}
	if stage == DiagnosticResponse && status != http.StatusOK {
		var envelope struct {
			Error struct {
				Code    int64 `json:"code"`
				Subcode int64 `json:"error_subcode"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) == nil {
			if envelope.Error.Code > 0 {
				d.ErrorCode = envelope.Error.Code
			}
			if envelope.Error.Subcode > 0 {
				d.ErrorSubcode = envelope.Error.Subcode
			}
		}
	}
	s.ObserveDiagnostic(d)
}

var errTransport = errors.New("protected transport outcome unconfirmed")
var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
var phoneIDPattern = regexp.MustCompile(`^[0-9]{1,100}$`)
var versionPattern = regexp.MustCompile(`^v[1-9][0-9]*\.[0-9]+$`)

// Send submits at most once. HTTP success means acceptance, never delivery.
// The durable owner fences network invocation and reconciles signed callbacks.
func (s MetaSender) Send(ctx context.Context, m delivery.Message) (delivery.Result, string, error) {
	if s.Resolve == nil || m.ID == "" || len(m.ID) > 512 || m.Tenant == "" || m.Account == "" || !phonePattern.MatchString(m.Sender) || strings.TrimSpace(m.Text) == "" || !utf8.ValidString(m.Text) || utf8.RuneCountInString(m.Text) > 4096 || (m.Channel != "" && m.Channel != "whatsapp") {
		s.observe(DiagnosticMessage, 0, nil)
		return delivery.Unknown, "", errTransport
	}
	c, e := s.Resolve(ctx, m)
	if e != nil || !phoneIDPattern.MatchString(c.PhoneID) || !versionPattern.MatchString(c.APIVersion) || strings.TrimSpace(c.AccessToken) == "" || strings.HasPrefix(strings.TrimSpace(c.AccessToken), "enc:") || strings.ContainsAny(c.AccessToken, "\r\n") {
		s.observe(DiagnosticCredentials, 0, nil)
		return delivery.Unknown, "", errTransport
	}
	body, e := json.Marshal(struct {
		Product     string         `json:"messaging_product"`
		Recipient   string         `json:"recipient_type"`
		To          string         `json:"to"`
		Type        string         `json:"type"`
		Text        map[string]any `json:"text"`
		Correlation string         `json:"biz_opaque_callback_data"`
	}{"whatsapp", "individual", strings.TrimPrefix(m.Sender, "+"), "text", map[string]any{"body": m.Text, "preview_url": false}, m.ID})
	if e != nil {
		s.observe(DiagnosticRequest, 0, nil)
		return delivery.Unknown, "", errTransport
	}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, "https://graph.facebook.com/"+c.APIVersion+"/"+c.PhoneID+"/messages", bytes.NewReader(body))
	if e != nil {
		s.observe(DiagnosticRequest, 0, nil)
		return delivery.Unknown, "", errTransport
	}
	request.Header.Set("Authorization", "Bearer "+c.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	transport := s.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(request)
	if e != nil {
		s.observe(DiagnosticTransport, 0, nil)
		return delivery.Unknown, "", errTransport
	}
	defer response.Body.Close()
	payload, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(payload) > 65536 {
		s.observe(DiagnosticResponse, response.StatusCode, nil)
		return delivery.Unknown, "", errTransport
	}
	if response.StatusCode != http.StatusOK {
		s.observe(DiagnosticResponse, response.StatusCode, payload)
		return delivery.Unknown, "", errTransport
	}
	var accepted struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if e = json.Unmarshal(payload, &accepted); e != nil || len(accepted.Messages) != 1 || accepted.Messages[0].ID == "" || len(accepted.Messages[0].ID) > 512 {
		s.observe(DiagnosticResponse, response.StatusCode, nil)
		return delivery.Unknown, "", errTransport
	}
	s.observe(DiagnosticAccepted, response.StatusCode, nil)
	return delivery.Accepted, accepted.Messages[0].ID, nil
}
