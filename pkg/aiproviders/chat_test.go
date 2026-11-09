package aiproviders

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestApprovedChatCompletion(t *testing.T) {
	for _, tc := range []struct{ kind, base, route, header, value string }{
		{"foundry", "https://fixture.openai.azure.com/openai/v1", "/openai/v1/chat/completions", "api-key", "fixture-key"},
		{"compatible", "https://fixture.example.test/v1", "/v1/chat/completions", "Authorization", "Bearer fixture-key"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			target, err := ParseEndpoint(Endpoint{ID: "test", Kind: tc.kind, BaseURL: tc.base})
			if err != nil {
				t.Fatal(err)
			}
			client := NewChat(target)
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != tc.route || r.Header.Get(tc.header) != tc.value {
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"model":"fixture-model"`) {
					t.Fatal("deployment missing")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":" hello "}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))}, nil
			})
			reply, err := client.Generate(context.Background(), Request{Credential: "fixture-key", Model: "fixture-model", MaxTokens: 16, Messages: []Message{{Role: "user", Content: "synthetic"}}})
			if err != nil || reply.Text != "hello" || reply.Usage == nil || reply.Usage.Input != 2 {
				t.Fatalf("%+v %v", reply, err)
			}
		})
	}
}

func TestEndpointRejectsUnexpectedDNSAnswers(t *testing.T) {
	for _, tc := range []struct {
		private string
		answers []string
		allowed bool
	}{
		{"", []string{"8.8.8.8"}, true},
		{"", []string{"8.8.8.8", "127.0.0.1"}, false},
		{"", []string{"168.63.129.16"}, false},
		{"", []string{"169.254.169.254"}, false},
		{"", []string{"::ffff:10.4.1.20"}, false},
		{"10.4.1.20", []string{"10.4.1.20"}, true},
		{"10.4.1.20", []string{"10.4.1.20", "10.4.1.21"}, false},
	} {
		endpoint, err := ParseEndpoint(Endpoint{ID: "test", Kind: "compatible", BaseURL: "https://gateway.example.test/v1", PrivateIP: tc.private})
		if err != nil {
			t.Fatal(err)
		}
		_, err = endpoint.dialAddress(tc.answers)
		if (err == nil) != tc.allowed {
			t.Fatalf("%v should be allowed=%v", tc.answers, tc.allowed)
		}
	}
}

func TestRejectsProviderBodyOnErrors(t *testing.T) {
	endpoint, err := ParseEndpoint(Endpoint{ID: "test", Kind: "compatible", BaseURL: "https://fixture.example.test/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client := NewChat(endpoint)
	calls := 0
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("sensitive provider body"))}, nil
	})
	_, err = client.Generate(context.Background(), Request{Credential: "fixture", Model: "m", MaxTokens: 4, Messages: []Message{{Role: "user", Content: "synthetic"}}})
	if !errors.Is(err, ErrRateLimited) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRefusesRedirectAndInvalidProviderResponses(t *testing.T) {
	endpoint, err := ParseEndpoint(Endpoint{ID: "test", Kind: "compatible", BaseURL: "https://fixture.example.test/v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status   int
		body     string
		expected error
	}{
		{307, "", ErrUnavailable},
		{200, "not JSON", ErrResponse},
		{200, strings.Repeat("x", maxResponse+1), ErrResponse},
		{200, `{"choices":[{"message":{"content":""}}]}`, ErrResponse},
		{200, `{"choices":[{"message":{"content":"unsafe","tool_calls":[{}]}}]}`, ErrResponse},
	} {
		client := NewChat(endpoint)
		calls := 0
		client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://unapproved.example.test"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		_, err := client.Generate(context.Background(), Request{Credential: "fixture", Model: "m", MaxTokens: 4, Messages: []Message{{Role: "user", Content: "synthetic"}}})
		if calls != 1 || !errors.Is(err, tc.expected) {
			t.Fatalf("status %d: calls=%d error=%v", tc.status, calls, err)
		}
	}
}
