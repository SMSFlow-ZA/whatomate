package handlers

import (
	"context"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// newUpgrader creates a WebSocket upgrader that validates origins against the
// configured allowed origins. If no origins are configured, all are allowed.
func newUpgrader(allowedOrigins map[string]bool) websocket.FastHTTPUpgrader {
	return websocket.FastHTTPUpgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
			origin := string(ctx.Request.Header.Peek("Origin"))
			return middleware.IsOriginAllowed(origin, allowedOrigins)
		},
	}
}

// wsUpgrader returns a WebSocket upgrader configured with the app's allowed origins.
func (a *App) wsUpgrader() websocket.FastHTTPUpgrader {
	allowedOrigins := middleware.ParseAllowedOrigins(a.Config.Server.AllowedOrigins)
	return newUpgrader(allowedOrigins)
}

// WebSocketHandler handles WebSocket connections.
// Authentication is performed via message-based auth after the upgrade:
// the client must send {"type":"auth","payload":{"token":"<jwt>"}} within 5 seconds.
func (a *App) WebSocketHandler(r *fastglue.Request) error {
	// Upgrade to WebSocket immediately (unauthenticated)
	up := a.wsUpgrader()
	err := up.Upgrade(r.RequestCtx, func(conn *websocket.Conn) {
		done := make(chan struct{})
		defer close(done)
		authenticate := a.validateWSTokenFn()
		validate := func(encoded string) (uuid.UUID, uuid.UUID, error) {
			userID, orgID, err := authenticate(encoded)
			if err == nil {
				claims, parseErr := middleware.ParseToken(a.Config.JWT.Secret, encoded, "ws")
				if parseErr != nil {
					return uuid.Nil, uuid.Nil, parseErr
				}
				go a.watchWSSession(done, claims, 5*time.Second, func() { _ = conn.Close() })
			}
			return userID, orgID, err
		}
		// Create unauthenticated client — auth happens via first message
		client := ws.NewUnauthenticatedClient(a.WSHub, conn, validate)

		// Start pumps in goroutines
		// Client self-registers with hub after successful auth message
		go client.WritePump()
		client.ReadPump() // Blocking - runs until connection closes
	})

	if err != nil {
		a.Log.Error("WebSocket upgrade failed", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "WebSocket upgrade failed", nil, "")
	}

	return nil
}

// Bound access for already established sockets after disable, logout or reset.
// The short WS ticket expires after authentication; the underlying session is
// rechecked until the connection closes, independently of that ticket's expiry.
func (a *App) watchWSSession(done <-chan struct{}, claims *middleware.JWTClaims, interval time.Duration, closeConnection func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := middleware.ValidateSession(ctx, a.Redis, claims)
			if err == nil {
				_, err = middleware.CurrentIdentity(a.DB.WithContext(ctx), claims)
			}
			cancel()
			if err != nil {
				closeConnection()
				return
			}
		}
	}
}

// validateWSTokenFn returns a function that validates a JWT token
// and returns user ID and organization ID.
func (a *App) validateWSTokenFn() ws.AuthenticateFn {
	return func(tokenString string) (uuid.UUID, uuid.UUID, error) {
		claims, err := middleware.ParseToken(a.Config.JWT.Secret, tokenString, "ws")
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		if claims.ID == "" {
			return uuid.Nil, uuid.Nil, jwt.ErrTokenInvalidClaims
		}
		if _, err := middleware.CurrentIdentity(a.DB, claims); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := middleware.ValidateSession(ctx, a.Redis, claims); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		owner, err := a.Redis.GetDel(ctx, "auth:ws:"+claims.ID).Result()
		if err != nil || owner != claims.UserID.String() {
			return uuid.Nil, uuid.Nil, jwt.ErrTokenInvalidClaims
		}

		return claims.UserID, claims.OrganizationID, nil
	}
}
