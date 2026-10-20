package protectedcarrier

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	smsflow "github.com/SMSFlow-ZA/whatomate-smsflow"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSMSPinnedProviderActualTLSAcceptsNamespacedUUID(t *testing.T) {
	submits := 0
	wireID := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/oauth/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic", "token_type": "Bearer", "expires_in": 3600})
		case "/api/smpp-gateway/v1/messages/submit":
			submits++
			var payload struct {
				Messages []struct {
					ID   string `json:"smppMessageId"`
					Body string `json:"body"`
				}
			}
			if e := json.NewDecoder(r.Body).Decode(&payload); e != nil || len(payload.Messages) != 1 {
				t.Error("invalid provider payload")
				w.WriteHeader(400)
				return
			}
			wireID = payload.Messages[0].ID
			if len(wireID) != 64 || payload.Messages[0].Body != delivery.MinimalSMSNotice || r.Header.Get("Idempotency-Key") != "" {
				t.Error("unsafe provider correlation or payload")
			}
			json.NewEncoder(w).Encode(map[string]any{"statusCode": 200, "results": []map[string]any{{"smppMessageId": wireID, "accepted": true}}})
		default:
			t.Errorf("unexpected provider endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	provider, e := smsflow.NewWithClient(server.URL, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	m := smsMessage()
	m.Account = uuid.NewString()
	m.ID = "private-case-ack:sms-fallback"
	sender := SMSSender{Resolve: func(context.Context, delivery.Message) (channels.Provider, channels.Connection, error) {
		return provider, channels.Connection{ID: m.Account, Credentials: channels.Credentials{"client_id": "synthetic", "client_secret": "synthetic"}, Config: map[string]string{"max_segments": "1"}}, nil
	}}
	result, providerID, e := sender.Send(context.Background(), m)
	if e != nil || result != delivery.Accepted || providerID != wireID || submits != 1 {
		t.Fatalf("pinned provider acceptance failed: %s %v submits%d", result, e, submits)
	}
	if !strings.HasPrefix(wireID, strings.ReplaceAll(m.Account, "-", "")) {
		t.Fatal("connection namespace not preserved")
	}
}
