package handlers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"gorm.io/driver/postgres"
)

// StartInboxLiveSubscriber opens one instance database listener per HTTP server.
// Standalone workers only write transactional hints; they need no listener.
func (a *App) StartInboxLiveSubscriber() error {
	if a.DB == nil || a.WSHub == nil {
		return errors.New("inbox live dependencies unavailable")
	}
	dialect, ok := a.DB.Dialector.(*postgres.Dialector)
	if !ok {
		return errors.New("inbox live requires PostgreSQL")
	}
	configuration, err := pgx.ParseConfig(dialect.DSN)
	if err != nil {
		return errors.New("inbox live database configuration unavailable")
	}
	configuration.RuntimeParams["application_name"] = "whatomate-inbox-live"
	a.inboxLiveMu.Lock()
	defer a.inboxLiveMu.Unlock()
	if a.inboxLiveCancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.inboxLiveCancel, a.inboxLiveDone = cancel, make(chan struct{})
	done := a.inboxLiveDone
	updates := inboxlive.Listen(ctx, inboxlive.PostgresConnector(configuration), inboxlive.ListenerOptions{})
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer close(done)
		var previous inboxlive.Reason
		for update := range updates {
			if update.Reset && update.Reason != previous {
				// Enumerated categories only: never payloads, DSNs or raw provider
				// errors. HTTP polling remains the independent recovery path.
				a.Log.Info("Inbox live transport state", "state", update.Reason)
				previous = update.Reason
			}
			a.WSHub.DeliverInboxUpdate(ctx, update)
		}
	}()
	return nil
}

func (a *App) StopInboxLiveSubscriber() {
	a.inboxLiveMu.Lock()
	cancel, done := a.inboxLiveCancel, a.inboxLiveDone
	a.inboxLiveMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	a.inboxLiveMu.Lock()
	if a.inboxLiveDone == done {
		a.inboxLiveCancel, a.inboxLiveDone = nil, nil
	}
	a.inboxLiveMu.Unlock()
}
