package main

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestHTTPServerHeaderBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		codeBytes   int
		cookieBytes int
		status      int
	}{
		{"small callback", 64, 64, http.StatusNoContent},
		{"Microsoft callback with existing session cookies", 8000, 3000, http.StatusNoContent},
		{"large bounded callback", 24000, 4000, http.StatusNoContent},
		{"oversized callback rejected", 64000, 1000, http.StatusRequestHeaderFieldsTooLarge},
		{"oversized cookies rejected", 64, 64000, http.StatusRequestHeaderFieldsTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reached atomic.Int32
			server := newHTTPServer(func(ctx *fasthttp.RequestCtx) {
				reached.Add(1)
				ctx.SetStatusCode(http.StatusNoContent)
			}, config.ServerConfig{ReadTimeout: 3, WriteTimeout: 3})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			served := make(chan error, 1)
			go func() { served <- server.Serve(listener) }()
			transport := &http.Transport{DisableKeepAlives: true}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			t.Cleanup(func() {
				transport.CloseIdleConnections()
				require.NoError(t, server.Shutdown())
				require.NoError(t, <-served)
			})
			// Synthetic values exercise the real parser; no OAuth provider is called.
			target := "http://" + listener.Addr().String() + "/api/auth/sso/microsoft/callback?code=" + strings.Repeat("A", tc.codeBytes) + "&state=synthetic-state"
			request, err := http.NewRequest(http.MethodGet, target, nil)
			require.NoError(t, err)
			request.Header.Set("Cookie", "synthetic_session="+strings.Repeat("B", tc.cookieBytes))
			request.Header.Set("X-Forwarded-Proto", "https")
			response, err := client.Do(request)
			if tc.status == http.StatusRequestHeaderFieldsTooLarge && err != nil {
				// The parser may reject and close while the client is still writing
				// oversized headers. Both paths must stop before the handler.
				require.True(t, errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE), err)
				require.Zero(t, reached.Load())
				return
			}
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, tc.status, response.StatusCode)
			if tc.status == http.StatusNoContent {
				require.EqualValues(t, 1, reached.Load())
			} else {
				require.Zero(t, reached.Load(), "rejected requests must not enter application handlers")
			}
		})
	}
}
