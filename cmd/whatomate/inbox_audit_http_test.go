package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestInboxAuditHTTPUsesFreshServerProvenanceAcrossKeepAliveRequests(t *testing.T) {
	app, router := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &fasthttp.Server{Handler: router.Handler()}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	t.Cleanup(func() {
		require.NoError(t, server.Shutdown())
		require.NoError(t, <-stopped)
	})
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	transport := &http.Transport{MaxConnsPerHost: 1}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Jar: jar, Transport: transport, Timeout: 8 * time.Second}
	base := "http://" + listener.Addr().String()
	body, err := json.Marshal(map[string]string{"email": user.Email, "password": "security-password-123"})
	require.NoError(t, err)
	login, err := http.NewRequest(http.MethodPost, base+"/api/auth/login", bytes.NewReader(body))
	require.NoError(t, err)
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-Request-ID", "synthetic-forged-id")
	login.Header.Set("X-Forwarded-For", "198.51.100.77")
	response, err := client.Do(login)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 200, response.StatusCode)
	request, err := http.NewRequest(http.MethodGet, base+"/api/roles", nil)
	require.NoError(t, err)
	request.Header.Set("X-Request-ID", "synthetic-forged-id")
	request.Header.Set("X-Forwarded-For", "198.51.100.99")
	var reused atomic.Bool
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}))
	response, err = client.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 403, response.StatusCode)
	require.True(t, reused.Load(), "the second request must use the existing TCP connection")
	var rows []models.AuditLog
	require.NoError(t, app.DB.Where("organization_id=? AND context_version=1", org.ID).Order("created_at,id").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, string(audit.SignIn), rows[0].Event)
	require.Equal(t, string(audit.AuthorizationDenied), rows[1].Event)
	require.Equal(t, "roles:read", rows[1].Permission)
	require.NotEqual(t, rows[0].RequestID, rows[1].RequestID)
	for _, row := range rows {
		require.NotEqual(t, "synthetic-forged-id", row.RequestID)
		require.Equal(t, "127.0.0.1", row.OriginIP)
		require.Equal(t, "peer", row.OriginSource)
		require.Equal(t, user.ID, row.UserID)
		require.True(t, row.ActorVerified)
		require.Empty(t, row.Changes)
	}
}
