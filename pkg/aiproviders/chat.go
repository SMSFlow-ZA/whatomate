package aiproviders

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Timeout     = 30 * time.Second
	maxRequest  = 64 * 1024
	maxResponse = 128 * 1024
	MaxTokens   = 2048
)

var (
	ErrEndpoint       = errors.New("endpoint_not_allowed")
	ErrInvalidRequest = errors.New("invalid_request")
	ErrTimeout        = errors.New("timeout")
	ErrCredential     = errors.New("authentication_failed")
	ErrAccessDenied   = errors.New("access_denied")
	ErrRateLimited    = errors.New("rate_limited")
	ErrUnavailable    = errors.New("provider_unavailable")
	ErrResponse       = errors.New("invalid_response")
)

// Provider is the v1 storage-independent text inference interface.
type Provider interface {
	Generate(context.Context, Request) (Reply, error)
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Request struct {
	Model      string
	Credential string `json:"-"`
	MaxTokens  int
	Messages   []Message
}
type Usage struct {
	Input  int `json:"prompt_tokens"`
	Output int `json:"completion_tokens"`
}
type Reply struct {
	Text  string
	Usage *Usage
}

type Chat struct {
	target Target
	http   *http.Client
}

func NewChat(t Target) *Chat {
	transport := &http.Transport{
		DialContext: t.dial, DisableKeepAlives: true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    5 * time.Second,
		MaxResponseHeaderBytes: 16 * 1024,
	}
	return &Chat{target: t, http: &http.Client{
		Transport: transport, Timeout: Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func ValidCredential(value string) bool {
	return value != "" && len(value) <= 4096 && strings.IndexFunc(value, func(r rune) bool { return r < 0x21 || r > 0x7e }) == -1
}

func (c *Chat) Generate(ctx context.Context, in Request) (Reply, error) {
	if !c.target.AllowsModel(in.Model) || !ValidCredential(in.Credential) ||
		in.MaxTokens < 1 || in.MaxTokens > MaxTokens || len(in.Messages) == 0 || len(in.Messages) > 64 {
		return Reply{}, ErrInvalidRequest
	}
	size := 0
	for _, message := range in.Messages {
		if (message.Role != "system" && message.Role != "user" && message.Role != "assistant") || !utf8.ValidString(message.Content) {
			return Reply{}, ErrInvalidRequest
		}
		size += len(message.Content)
		if size > maxRequest {
			return Reply{}, ErrInvalidRequest
		}
	}
	payload := struct {
		Model               string    `json:"model"`
		Messages            []Message `json:"messages"`
		MaxTokens           int       `json:"max_tokens,omitempty"`
		MaxCompletionTokens int       `json:"max_completion_tokens,omitempty"`
	}{Model: in.Model, Messages: in.Messages}
	if c.target.Kind() == Foundry {
		payload.MaxCompletionTokens = in.MaxTokens
	} else {
		payload.MaxTokens = in.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxRequest {
		return Reply{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.target.url.String(), bytes.NewReader(body))
	if err != nil {
		return Reply{}, ErrInvalidRequest
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.target.Kind() == Foundry {
		req.Header.Set("api-key", in.Credential)
	} else {
		req.Header.Set("Authorization", "Bearer "+in.Credential)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Reply{}, safeError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return Reply{}, ErrRateLimited
	case http.StatusUnauthorized:
		return Reply{}, ErrCredential
	case http.StatusForbidden:
		return Reply{}, ErrAccessDenied
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return Reply{}, ErrInvalidRequest
	default:
		return Reply{}, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return Reply{}, safeError(err)
	}
	if len(raw) > maxResponse || !utf8.Valid(raw) {
		return Reply{}, ErrResponse
	}
	var result struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string            `json:"content"`
				Tools   []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Choices) != 1 {
		return Reply{}, ErrResponse
	}
	choice := result.Choices[0]
	answer := strings.TrimSpace(choice.Message.Content)
	if answer == "" || len(answer) > 4096 || len(choice.Message.Tools) > 0 || choice.Finish == "content_filter" || choice.Finish == "tool_calls" {
		return Reply{}, ErrResponse
	}
	return Reply{Text: answer, Usage: result.Usage}, nil
}

func safeError(err error) error {
	var failure net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &failure) && failure.Timeout()) {
		return ErrTimeout
	}
	if errors.Is(err, ErrEndpoint) {
		return ErrEndpoint
	}
	return ErrUnavailable
}
