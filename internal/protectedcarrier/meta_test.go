package protectedcarrier

import (
	"context"
	"encoding/json"
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
