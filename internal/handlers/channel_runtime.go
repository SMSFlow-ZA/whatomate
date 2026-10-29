package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/channelregistry"
	"github.com/shridarpatil/whatomate/internal/websocket"
)

// InitializeChannels installs compiled modules; no executable code is accepted
// from a customer. New connections remain impossible without encryption.
func (a *App) InitializeChannels() error {
	registry, err := channelregistry.New(a.Config.Channels.SMSFlowEndpoint, strings.Split(a.Config.Channels.SMSFlowReplyRoutes, ",")...)
	if err != nil {
		return err
	}
	a.Channels = &channelapp.Service{DB: a.DB, Providers: registry, EncryptionKey: a.Config.App.EncryptionKey, StopDispatch: a.Config.Channels.StopDispatch, Notify: func(org, _ uuid.UUID) {
		if a.WSHub == nil {
			return
		}
		// Membership alone does not authorize this contact or its history. The
		// browser re-fetches through current role/assignment/session checks.
		a.WSHub.BroadcastToOrg(org, websocket.WSMessage{Type: websocket.TypeStatusUpdate, Payload: map[string]any{"channel_invalidation": true}})
	}}
	return nil
}

func (a *App) StartChannelWorker() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if a.Channels == nil {
		return cancel
	}
	a.wg.Add(2)
	go func() {
		defer a.wg.Done()
		a.Channels.Run(ctx, 5*time.Second, func(error) { a.Log.Error("Channel worker failed; pending work retained") })
	}()
	go func() {
		defer a.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := a.ProcessSMSBotJobs(ctx, now); err != nil && ctx.Err() == nil {
					a.Log.Error("SMS bot worker failed; pending work retained")
				}
			}
		}
	}()
	return cancel
}
