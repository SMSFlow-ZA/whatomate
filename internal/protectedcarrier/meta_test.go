package protectedcarrier

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func validMessage() delivery.Message {
	return delivery.Message{ID: "opaque-intent", Tenant: "org", Account: "account", Sender: "+27820000001", Text: "Synthetic notice"}
}

func TestMetaDiagnosticsContainOnlyBoundedFields(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       Diagnostic
	}{
		{"provider error", `{"error":{"message":"Bearer secret; private content; https://private.invalid/phone-id","code":190,"error_subcode":463,"fbtrace_id":"private-trace","error_data":{"details":"private content"}}}`, 401, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 401, ErrorCode: 190, ErrorSubcode: 463}},
		{"malformed", `{"error":{"code":190}`, 502, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 502}},
		{"string code", `{"error":{"code":"secret","error_subcode":463}}`, 400, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 400}},
		{"string subcode", `{"error":{"code":190,"error_subcode":"secret"}}`, 400, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 400}},
		{"fractional code", `{"error":{"code":1.5,"error_subcode":463}}`, 400, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 400}},
		{"overflow code", `{"error":{"code":999999999999999999999999}}`, 400, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 400}},
		{"negative codes", `{"error":{"code":-190,"error_subcode":-463}}`, 400, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 400}},
		{"oversized", strings.Repeat("private content", 6000), 500, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 500}},
		{"malformed success", `{"messages":[]}`, 200, Diagnostic{Stage: DiagnosticResponse, HTTPStatus: 200}},
		{"accepted", `{"messages":[{"id":"private-provider-id"}]}`, 200, Diagnostic{Stage: DiagnosticAccepted, HTTPStatus: 200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed []Diagnostic
			calls := 0
			s := MetaSender{Resolve: creds, ObserveDiagnostic: func(d Diagnostic) { observed = append(observed, d) }, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			result, id, err := s.Send(context.Background(), validMessage())
			if len(observed) != 1 || observed[0] != tc.want || calls != 1 {
				t.Fatalf("diagnostics=%+v calls=%d", observed, calls)
			}
			encoded, marshalErr := json.Marshal(observed)
			if marshalErr != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private") {
				t.Fatal("untrusted provider data escaped diagnostic boundary")
			}
			if tc.want.Stage == DiagnosticAccepted {
				if result != delivery.Accepted || id != "private-provider-id" || err != nil {
					t.Fatal("acceptance semantics changed")
				}
			} else if result != delivery.Unknown || id != "" || err != errTransport {
				t.Fatal("failure must remain unknown without retry")
			}
		})
	}
}

func TestMetaDiagnosticsRejectUndecryptedCredentialsBeforeNetwork(t *testing.T) {
	for _, token := range []string{"enc:private-ciphertext", "  enc:private-ciphertext"} {
		var observed []Diagnostic
		s := MetaSender{Resolve: func(ctx context.Context, m delivery.Message) (Credentials, error) {
			c, _ := creds(ctx, m)
			c.AccessToken = token
			return c, nil
		}, ObserveDiagnostic: func(d Diagnostic) { observed = append(observed, d) }, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("encrypted credential reached network")
			return nil, nil
		})}
		result, id, err := s.Send(context.Background(), validMessage())
		if result != delivery.Unknown || id != "" || err != errTransport || len(observed) != 1 || observed[0] != (Diagnostic{Stage: DiagnosticCredentials}) {
			t.Fatal("encrypted credential must remain unknown with bounded credential diagnostic")
		}
	}
}

func TestMetaDiagnosticsNeverExposeTransportOrResolverErrors(t *testing.T) {
	for _, stage := range []DiagnosticStage{DiagnosticCredentials, DiagnosticTransport} {
		var observed []Diagnostic
		s := MetaSender{Resolve: creds, ObserveDiagnostic: func(d Diagnostic) { observed = append(observed, d) }, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("https://private.invalid/secret-token private-provider-error")
		})}
		if stage == DiagnosticCredentials {
			s.Resolve = func(context.Context, delivery.Message) (Credentials, error) {
				return Credentials{}, errors.New("private resolver credential")
			}
		}
		result, id, err := s.Send(context.Background(), validMessage())
		if result != delivery.Unknown || id != "" || err != errTransport || len(observed) != 1 || observed[0] != (Diagnostic{Stage: stage}) {
			t.Fatal("untrusted error must remain unknown with bounded diagnostic")
		}
	}
}
func creds(context.Context, delivery.Message) (Credentials, error) {
	return Credentials{PhoneID: "12345", APIVersion: "v25.0", AccessToken: "synthetic-test-token"}, nil
}
func TestMetaAcceptanceIsNotDelivery(t *testing.T) {
	calls := 0
	s := MetaSender{Resolve: creds, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://graph.facebook.com/v25.0/12345/messages" {
			t.Fatal("wrong exact Meta endpoint")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Fatal("credentials absent")
		}
		var b map[string]any
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Fatal(e)
		}
		if b["biz_opaque_callback_data"] != "opaque-intent" || b["to"] != "27820000001" || b["type"] != "text" {
			t.Fatal("missing safe correlation or canonical recipient")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"messages":[{"id":"provider-id"}]}`)), Header: make(http.Header)}, nil
	})}
	result, id, e := s.Send(context.Background(), validMessage())
	if e != nil || result != delivery.Accepted || id != "provider-id" || calls != 1 {
		t.Fatalf("result=%s id=%q error=%v calls=%d", result, id, e, calls)
	}
}
func TestMetaUnknownNeverRetriesOrFollowsRedirects(t *testing.T) {
	for _, body := range []string{`{}`, `{"messages":[{"id":""}]}`, `not json`, strings.Repeat("x", 65537)} {
		calls := 0
		s := MetaSender{Resolve: creds, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		result, _, _ := s.Send(context.Background(), validMessage())
		if result != delivery.Unknown || calls != 1 {
			t.Fatal("ambiguous response must be one-shot unknown")
		}
	}
	calls := 0
	s := MetaSender{Resolve: creds, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 307, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://invalid.example/steal"}}}, nil
	})}
	result, _, _ := s.Send(context.Background(), validMessage())
	if result != delivery.Unknown || calls != 1 {
		t.Fatal("redirect followed or retried")
	}
}
func TestMetaInvalidBindingsAndMessagesDoNotSend(t *testing.T) {
	for _, c := range []Credentials{{PhoneID: "../other", APIVersion: "v25.0", AccessToken: "secret"}, {PhoneID: "123", APIVersion: "v25.0/other", AccessToken: "secret"}, {PhoneID: "123", APIVersion: "v25.0"}} {
		s := MetaSender{Resolve: func(context.Context, delivery.Message) (Credentials, error) { return c, nil }, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid binding reached network")
			return nil, nil
		})}
		result, _, _ := s.Send(context.Background(), validMessage())
		if result != delivery.Unknown {
			t.Fatal("invalid binding accepted")
		}
	}
	for _, phone := range []string{"0820000001", "+27 820000001", "+0123456789", "+27820000001/path"} {
		m := validMessage()
		m.Sender = phone
		s := MetaSender{Resolve: creds, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid recipient reached network")
			return nil, nil
		})}
		result, _, _ := s.Send(context.Background(), m)
		if result != delivery.Unknown {
			t.Fatal("invalid recipient accepted")
		}
	}
}
